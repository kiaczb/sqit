import * as vscode from 'vscode';
import { findRepoRoot, Sqit, StatusJson } from './sqit';
import { TablesProvider } from './tree';
import { TableViews } from './tableView';

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const sqit = new Sqit(await findRepoRoot());
  const working = new TablesProvider(sqit, 'working');
  const upstream = new TablesProvider(sqit, 'upstream');
  const views = new TableViews(sqit, context.extensionUri);
  const statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 50);
  statusBar.command = 'sqit.status';
  statusBar.name = 'SQit';

  const workingView = vscode.window.createTreeView('sqit.tables', { treeDataProvider: working, showCollapseAll: true });
  const upstreamView = vscode.window.createTreeView('sqit.upstream', { treeDataProvider: upstream, showCollapseAll: true });

  let refreshing: Promise<void> | undefined;
  const refresh = (): Promise<void> => {
    if (refreshing) return refreshing;
    refreshing = (async () => {
      if (!sqit.root) sqit.root = await findRepoRoot();
      if (!sqit.root) {
        working.update(undefined);
        upstream.update(undefined);
        statusBar.hide();
        return;
      }
      try {
        const st: StatusJson = await sqit.status();
        working.update(st);
        upstream.update(st);
        const w = st.workingTree;
        workingView.description = st.dirty ? `+${w.added} ~${w.modified} -${w.deleted}` : 'clean';
        upstreamView.description = st.relation.toLowerCase();
        statusBar.text = st.dirty
          ? `$(database) sqit +${w.added} ~${w.modified} -${w.deleted}`
          : '$(database) sqit clean';
        statusBar.tooltip = `SQit – HEAD ${st.head.slice(0, 12) || '(none)'} · upstream ${st.upstream.slice(0, 12) || '(none)'} · ${st.relation}`;
        statusBar.show();
        await views.refreshAll();
      } catch (e) {
        const msg = String((e as Error).message ?? e);
        working.update(undefined, msg);
        upstream.update(undefined, msg);
        statusBar.text = '$(database) sqit: error';
        statusBar.tooltip = msg;
        statusBar.show();
      }
    })().finally(() => (refreshing = undefined));
    return refreshing;
  };

  const runAndRefresh = async (args: string[], okMessage: string): Promise<void> => {
    await vscode.window.withProgress({ location: vscode.ProgressLocation.Window, title: `sqit ${args[0]}` }, async () => {
      const r = await sqit.runLogged(args);
      if (r.code === 0) {
        vscode.window.setStatusBarMessage(okMessage, 4000);
      } else if (r.code === 127) {
        vscode.window.showErrorMessage(`SQit: cannot run '${sqit.executable}' – set "sqit.executable" in the settings.`);
      } else {
        const text = (r.stdout + '\n' + r.stderr).trim();
        const conflicts = /CONFLICTS: (\d+)/.exec(text);
        const first = text.split('\n').find((l) => l.trim() && !l.startsWith('Pull') && !l.startsWith('===')) ?? '';
        const summary = conflicts ? `${conflicts[1]} conflict(s) – nothing was applied` : first;
        vscode.window.showWarningMessage(`sqit ${args.join(' ')}: ${summary}`, 'Show output').then((pick) => {
          if (pick) sqit.output.show(true);
        });
      }
      await refresh();
    });
  };

  context.subscriptions.push(
    workingView,
    upstreamView,
    statusBar,
    sqit.output,
    vscode.commands.registerCommand('sqit.refresh', refresh),
    vscode.commands.registerCommand('sqit.openTable', (table: string, mode: 'working' | 'upstream' = 'working', key?: string) =>
      views.open(table, mode, key)
    ),
    vscode.commands.registerCommand('sqit.commit', async () => {
      const message = await vscode.window.showInputBox({ prompt: 'Commit message', placeHolder: 'Update test data' });
      if (!message) return;
      await runAndRefresh(['commit', '-m', message], 'sqit: committed');
    }),
    vscode.commands.registerCommand('sqit.fetch', () => runAndRefresh(['fetch'], 'sqit: fetched upstream')),
    vscode.commands.registerCommand('sqit.pullDryRun', () => runAndRefresh(['pull', '--dry-run'], 'sqit: dry run finished (see output)')),
    vscode.commands.registerCommand('sqit.pull', async () => {
      const pick = await vscode.window.showWarningMessage(
        'Pull merges the upstream (TEST) snapshot into the LOCAL database. Continue?',
        { modal: true },
        'Pull'
      );
      if (pick !== 'Pull') return;
      await runAndRefresh(['pull'], 'sqit: pulled');
    }),
    vscode.commands.registerCommand('sqit.pullCreateTables', async () => {
      const pick = await vscode.window.showWarningMessage(
        'Pull merges the upstream (TEST) snapshot into the LOCAL database and CREATEs tables that exist only upstream. Continue?',
        { modal: true },
        'Pull'
      );
      if (pick !== 'Pull') return;
      await runAndRefresh(['pull', '--create-missing-tables'], 'sqit: pulled');
    }),
    vscode.commands.registerCommand('sqit.status', () => runAndRefresh(['status'], 'sqit: status in output')),
    vscode.commands.registerCommand('sqit.diff', () => sqit.runLogged(['diff'])),
    vscode.commands.registerCommand('sqit.log', () => sqit.runLogged(['log'])),
    vscode.window.onDidChangeWindowState((s) => {
      if (s.focused && vscode.workspace.getConfiguration('sqit').get<boolean>('refreshOnFocus', true)) void refresh();
    }),
    vscode.workspace.onDidChangeWorkspaceFolders(() => {
      sqit.root = undefined;
      void refresh();
    })
  );

  // Refresh when the repository itself changes (commit/pull from a terminal).
  const watcher = vscode.workspace.createFileSystemWatcher('**/.sqit/{HEAD,refs/**,config.json}');
  watcher.onDidChange(() => void refresh());
  watcher.onDidCreate(() => void refresh());
  watcher.onDidDelete(() => void refresh());
  context.subscriptions.push(watcher);

  await refresh();
}

export function deactivate(): void {}
