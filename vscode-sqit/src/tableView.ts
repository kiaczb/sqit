import * as vscode from 'vscode';
import { Sqit, TableJson } from './sqit';

type Mode = 'working' | 'upstream';

/** One webview panel per table, showing its rows with GitHub-style changes. */
export class TableViews {
  private readonly panels = new Map<string, vscode.WebviewPanel>();
  private readonly modes = new Map<string, Mode>();

  constructor(private readonly sqit: Sqit, private readonly extensionUri: vscode.Uri) {}

  async open(table: string, mode: Mode = 'working', focusKey?: string): Promise<void> {
    let panel = this.panels.get(table);
    this.modes.set(table, mode);
    if (!panel) {
      panel = vscode.window.createWebviewPanel('sqit.table', `SQit: ${table}`, vscode.ViewColumn.Active, {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [this.extensionUri],
      });
      panel.iconPath = vscode.Uri.joinPath(this.extensionUri, 'media', 'sqit.svg');
      panel.webview.html = html(panel.webview);
      panel.onDidDispose(() => this.panels.delete(table));
      panel.webview.onDidReceiveMessage(async (msg: { type: string; mode?: Mode }) => {
        if (msg.type === 'refresh') await this.load(table);
        if (msg.type === 'mode' && msg.mode) {
          this.modes.set(table, msg.mode);
          await this.load(table);
        }
      });
      this.panels.set(table, panel);
    } else {
      panel.reveal(vscode.ViewColumn.Active);
    }
    await this.load(table, focusKey);
  }

  /** Reloads every open panel (after commit / pull / refresh). */
  async refreshAll(): Promise<void> {
    for (const table of this.panels.keys()) await this.load(table);
  }

  private async load(table: string, focusKey?: string): Promise<void> {
    const panel = this.panels.get(table);
    if (!panel) return;
    const mode = this.modes.get(table) ?? 'working';
    try {
      const data: TableJson = await this.sqit.table(table, mode);
      await panel.webview.postMessage({ type: 'data', table: data, focusKey });
    } catch (e) {
      await panel.webview.postMessage({ type: 'error', message: String((e as Error).message ?? e), mode });
    }
  }
}

export function html(webview: vscode.Webview): string {
  const nonce = Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline' ${webview.cspSource}; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SQit table</title>
<style>
  :root {
    --add-bg: var(--vscode-diffEditor-insertedLineBackground, rgba(46,160,67,.15));
    --add-text-bg: var(--vscode-diffEditor-insertedTextBackground, rgba(46,160,67,.4));
    --del-bg: var(--vscode-diffEditor-removedLineBackground, rgba(248,81,73,.15));
    --del-text-bg: var(--vscode-diffEditor-removedTextBackground, rgba(248,81,73,.4));
    --mod-bg: color-mix(in srgb, var(--vscode-editorGutter-modifiedBackground, #e2c08d) 12%, transparent);
    --border: var(--vscode-panel-border, var(--vscode-widget-border, #444));
  }
  body { font-family: var(--vscode-font-family); font-size: var(--vscode-font-size); color: var(--vscode-foreground); background: var(--vscode-editor-background); margin: 0; padding: 0; }
  .toolbar { position: sticky; top: 0; z-index: 3; display: flex; flex-wrap: wrap; gap: 10px; align-items: center; padding: 8px 12px; background: var(--vscode-editor-background); border-bottom: 1px solid var(--border); }
  .toolbar h1 { font-size: 1.1em; margin: 0 8px 0 0; font-weight: 600; }
  .toolbar label { display: inline-flex; align-items: center; gap: 4px; cursor: pointer; }
  .toolbar input[type=text] { background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, transparent); padding: 3px 6px; border-radius: 2px; min-width: 160px; }
  .toolbar button { background: var(--vscode-button-secondaryBackground, #3a3d41); color: var(--vscode-button-secondaryForeground, #fff); border: none; padding: 4px 10px; border-radius: 2px; cursor: pointer; }
  .toolbar button:hover { background: var(--vscode-button-secondaryHoverBackground, #45494e); }
  .counts { display: inline-flex; gap: 10px; margin-left: auto; font-variant-numeric: tabular-nums; }
  .counts .add { color: var(--vscode-gitDecoration-addedResourceForeground, #3fb950); }
  .counts .mod { color: var(--vscode-gitDecoration-modifiedResourceForeground, #d29922); }
  .counts .del { color: var(--vscode-gitDecoration-deletedResourceForeground, #f85149); }
  .mode { display: inline-flex; border: 1px solid var(--border); border-radius: 3px; overflow: hidden; }
  .mode label { padding: 3px 8px; gap: 0; }
  .mode label.on { background: var(--vscode-button-background, #0e639c); color: var(--vscode-button-foreground, #fff); }
  .mode input { display: none; }
  table { border-collapse: separate; border-spacing: 0; width: max-content; min-width: 100%; font-family: var(--vscode-editor-font-family); font-size: var(--vscode-editor-font-size); }
  th, td { padding: 4px 10px; border-bottom: 1px solid var(--border); border-right: 1px solid var(--border); white-space: pre; text-align: left; vertical-align: top; }
  th { position: sticky; top: 41px; z-index: 2; background: var(--vscode-editorGroupHeader-tabsBackground, var(--vscode-sideBar-background)); font-weight: 600; }
  th.pk::after { content: " \\1F511"; font-size: .8em; opacity: .7; }
  td.status, th.status { width: 1.2em; text-align: center; font-weight: 700; color: var(--vscode-descriptionForeground); position: sticky; left: 0; background: var(--vscode-editor-background); z-index: 1; }
  tr.added td { background: var(--add-bg); }
  tr.added td.status { color: var(--vscode-gitDecoration-addedResourceForeground, #3fb950); }
  tr.deleted td { background: var(--del-bg); text-decoration: line-through; opacity: .85; }
  tr.deleted td.status { color: var(--vscode-gitDecoration-deletedResourceForeground, #f85149); text-decoration: none; }
  tr.modified td { background: var(--mod-bg); }
  tr.modified td.status { color: var(--vscode-gitDecoration-modifiedResourceForeground, #d29922); }
  td.changed { background: transparent !important; }
  .old { background: var(--del-text-bg); text-decoration: line-through; padding: 0 3px; border-radius: 2px; margin-right: 6px; }
  .new { background: var(--add-text-bg); padding: 0 3px; border-radius: 2px; }
  .null { opacity: .55; font-style: italic; }
  tr.focus td { outline: 2px solid var(--vscode-focusBorder); outline-offset: -2px; }
  tr:hover td { filter: brightness(1.12); }
  .empty, .error { padding: 24px; color: var(--vscode-descriptionForeground); }
  .error { color: var(--vscode-errorForeground); white-space: pre-wrap; }
  .legend { display: inline-flex; gap: 10px; font-size: .9em; color: var(--vscode-descriptionForeground); }
  .legend span { padding: 0 6px; border-radius: 2px; }
</style>
</head>
<body>
<div class="toolbar">
  <h1 id="title">…</h1>
  <span class="mode">
    <label id="mode-working" class="on"><input type="radio" name="mode" value="working" checked>LOCAL vs HEAD</label>
    <label id="mode-upstream"><input type="radio" name="mode" value="upstream">UPSTREAM vs HEAD</label>
  </span>
  <label><input type="checkbox" id="onlyChanges"> only changes</label>
  <input type="text" id="filter" placeholder="filter…">
  <button id="refresh" title="Re-read the database">↻ Refresh</button>
  <span class="legend"><span style="background:var(--add-bg)">added</span><span style="background:var(--mod-bg)">modified</span><span style="background:var(--del-bg)">deleted</span></span>
  <span class="counts" id="counts"></span>
</div>
<div id="content"><div class="empty">Loading…</div></div>
<script nonce="${nonce}">
  const vscode = acquireVsCodeApi();
  let data = null, focusKey = null;
  const $ = (id) => document.getElementById(id);
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const cell = (c) => c.null ? '<span class="null">NULL</span>' : esc(c.value);
  const oldCell = (c) => c.oldNull ? '<span class="null">NULL</span>' : esc(c.old);

  function render() {
    if (!data) return;
    $('title').textContent = data.table;
    $('mode-working').classList.toggle('on', data.mode === 'working');
    $('mode-upstream').classList.toggle('on', data.mode === 'upstream');
    $('counts').innerHTML = data.counts.total + ' rows' +
      ' <span class="add">+' + data.counts.added + '</span>' +
      ' <span class="mod">~' + data.counts.modified + '</span>' +
      ' <span class="del">-' + data.counts.deleted + '</span>';
    const only = $('onlyChanges').checked;
    const q = $('filter').value.trim().toLowerCase();
    const rows = data.rows.filter((r) => {
      if (only && r.status === 'unchanged') return false;
      if (!q) return true;
      return data.columns.some((col) => {
        const c = r.cells[col] || {};
        return String(c.value ?? '').toLowerCase().includes(q) || String(c.old ?? '').toLowerCase().includes(q);
      });
    });
    if (rows.length === 0) {
      $('content').innerHTML = '<div class="empty">' + (data.rows.length === 0 ? 'The table is empty.' : 'No rows match.') + '</div>';
      return;
    }
    const pk = new Set(data.primaryKey);
    let h = '<table><thead><tr><th class="status"></th>';
    for (const col of data.columns) h += '<th class="' + (pk.has(col) ? 'pk' : '') + '">' + esc(col) + '</th>';
    h += '</tr></thead><tbody>';
    const mark = { added: '+', modified: '~', deleted: '−', unchanged: '' };
    for (const r of rows) {
      h += '<tr class="' + r.status + (r.key === focusKey ? ' focus' : '') + '" data-key="' + esc(r.key) + '" title="' + esc(r.id) + '">';
      h += '<td class="status">' + mark[r.status] + '</td>';
      for (const col of data.columns) {
        const c = r.cells[col] || { value: '' };
        if (c.changed) h += '<td class="changed"><span class="old">' + oldCell(c) + '</span><span class="new">' + cell(c) + '</span></td>';
        else h += '<td>' + cell(c) + '</td>';
      }
      h += '</tr>';
    }
    h += '</tbody></table>';
    $('content').innerHTML = h;
    if (focusKey) {
      const el = document.querySelector('tr.focus');
      if (el) el.scrollIntoView({ block: 'center' });
      focusKey = null;
    }
  }

  window.addEventListener('message', (ev) => {
    const msg = ev.data;
    if (msg.type === 'data') { data = msg.table; if (msg.focusKey) focusKey = msg.focusKey; render(); }
    if (msg.type === 'error') {
      $('mode-working').classList.toggle('on', msg.mode === 'working');
      $('mode-upstream').classList.toggle('on', msg.mode === 'upstream');
      $('content').innerHTML = '<div class="error">' + esc(msg.message) + '</div>';
    }
  });
  $('onlyChanges').addEventListener('change', render);
  $('filter').addEventListener('input', render);
  $('refresh').addEventListener('click', () => vscode.postMessage({ type: 'refresh' }));
  for (const radio of document.querySelectorAll('input[name=mode]')) {
    radio.addEventListener('change', (e) => vscode.postMessage({ type: 'mode', mode: e.target.value }));
  }
</script>
</body>
</html>`;
}
