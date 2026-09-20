import * as vscode from 'vscode';
import { ChangeSet, RecordChange, Sqit, StatusJson } from './sqit';

type Mode = 'working' | 'upstream';

/** A table node or a changed-record node. */
export class Node extends vscode.TreeItem {
  constructor(
    public readonly kind: 'table' | 'record' | 'info',
    public readonly table: string,
    public readonly mode: Mode,
    label: string,
    public readonly change?: RecordChange
  ) {
    super(label, kind === 'table' ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None);
    this.contextValue = kind;
    if (kind === 'table') {
      this.iconPath = new vscode.ThemeIcon('table');
      this.command = { command: 'sqit.openTable', title: 'Open table', arguments: [table, mode] };
    } else if (kind === 'record' && change) {
      const icon = { added: 'diff-added', modified: 'diff-modified', deleted: 'diff-removed' }[change.kind];
      const color = {
        added: 'gitDecoration.addedResourceForeground',
        modified: 'gitDecoration.modifiedResourceForeground',
        deleted: 'gitDecoration.deletedResourceForeground',
      }[change.kind];
      this.iconPath = new vscode.ThemeIcon(icon, new vscode.ThemeColor(color));
      this.command = { command: 'sqit.openTable', title: 'Open table', arguments: [table, mode, change.key] };
      if (change.fields?.length) {
        this.description = change.fields.map((f) => f.column).join(', ');
        this.tooltip = change.fields.map((f) => `${f.column}: ${f.old} -> ${f.new}`).join('\n');
      }
    } else {
      this.iconPath = new vscode.ThemeIcon('info');
    }
  }
}

/**
 * Tree of tracked tables. In "working" mode it shows LOCAL vs HEAD, in
 * "upstream" mode the last fetched TEST snapshot vs HEAD.
 */
export class TablesProvider implements vscode.TreeDataProvider<Node> {
  private readonly emitter = new vscode.EventEmitter<Node | undefined>();
  readonly onDidChangeTreeData = this.emitter.event;
  status: StatusJson | undefined;
  error: string | undefined;

  constructor(private readonly sqit: Sqit, private readonly mode: Mode) {}

  update(status: StatusJson | undefined, error?: string): void {
    this.status = status;
    this.error = error;
    this.emitter.fire(undefined);
  }

  private changeSet(): ChangeSet | undefined {
    if (!this.status) return undefined;
    return this.mode === 'working' ? this.status.workingTree : this.status.upstreamChanges;
  }

  getTreeItem(n: Node): vscode.TreeItem {
    return n;
  }

  getChildren(n?: Node): Node[] {
    if (this.error) return [new Node('info', '', this.mode, this.error)];
    if (!this.status) return [];
    const cs = this.changeSet();
    if (n === undefined) {
      if (this.mode === 'upstream' && !cs) {
        return [new Node('info', '', this.mode, 'No upstream snapshot yet – run Fetch')];
      }
      const tables = this.status.tables?.length ? this.status.tables : this.sqit.tables();
      const nodes = tables.map((t) => {
        const changes = (cs?.changes ?? []).filter((c) => c.table === t);
        const a = changes.filter((c) => c.kind === 'added').length;
        const m = changes.filter((c) => c.kind === 'modified').length;
        const d = changes.filter((c) => c.kind === 'deleted').length;
        const node = new Node('table', t, this.mode, t);
        node.description = changes.length ? `+${a} ~${m} -${d}` : '';
        node.collapsibleState = changes.length
          ? vscode.TreeItemCollapsibleState.Expanded
          : vscode.TreeItemCollapsibleState.None;
        if (changes.length) {
          node.iconPath = new vscode.ThemeIcon('table', new vscode.ThemeColor('gitDecoration.modifiedResourceForeground'));
        }
        return node;
      });
      const schema = cs?.schemaChanges ?? [];
      if (schema.length) {
        nodes.unshift(new Node('info', '', this.mode, `${schema.length} schema change(s): ${schema.map((s) => `${s.kind} ${s.table}${s.column ? '.' + s.column : ''}`).join('; ')}`));
      }
      return nodes;
    }
    if (n.kind === 'table') {
      return (cs?.changes ?? [])
        .filter((c) => c.table === n.table)
        .map((c) => new Node('record', n.table, this.mode, `${c.kind}: ${displayKey(c)}`, c));
    }
    return [];
  }
}

function displayKey(c: RecordChange): string {
  // key is the canonical JSON array, e.g. [42] or [10,55]
  try {
    const v = JSON.parse(c.key) as unknown[];
    return v.map((x) => (x === null ? 'NULL' : String(x))).join(',');
  } catch {
    return c.key;
  }
}
