import * as vscode from 'vscode';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

/** Result of running the sqit CLI. */
export interface RunResult {
  code: number;
  stdout: string;
  stderr: string;
}

/** `sqit status --json` */
export interface StatusJson {
  head: string;
  upstream: string;
  base: string;
  relation: string;
  dirty: boolean;
  workingTree: ChangeSet;
  upstreamChanges?: ChangeSet & { aheadBy: number; localAheadBy: number };
}

export interface ChangeSet {
  added: number;
  modified: number;
  deleted: number;
  changes: RecordChange[];
  schemaChanges: SchemaChange[] | null;
}

export interface RecordChange {
  table: string;
  key: string;
  kind: 'added' | 'modified' | 'deleted';
  fields?: { column: string; old: string; new: string }[];
}

export interface SchemaChange {
  kind: string;
  table: string;
  column?: string;
  from?: string;
  to?: string;
}

/** `sqit table <name> --json` */
export interface TableJson {
  table: string;
  mode: 'working' | 'upstream';
  primaryKey: string[];
  columns: string[];
  rows: TableRow[];
  counts: { total: number; added: number; modified: number; deleted: number };
}

export interface TableRow {
  key: string;
  id: string;
  status: 'unchanged' | 'added' | 'modified' | 'deleted';
  cells: Record<string, Cell>;
}

export interface Cell {
  value: string;
  null?: boolean;
  changed?: boolean;
  old?: string;
  oldNull?: boolean;
}

/** Wraps the sqit CLI for one repository root. */
export class Sqit {
  readonly output = vscode.window.createOutputChannel('SQit');

  constructor(public root: string | undefined) {}

  get executable(): string {
    const configured = vscode.workspace.getConfiguration('sqit').get<string>('executable') || 'sqit';
    if (configured !== 'sqit') return configured;
    // Fall back to the `go install` location when plain "sqit" is not
    // resolvable (VS Code may have been started before PATH was updated).
    if (this.resolved === undefined) {
      const exe = process.platform === 'win32' ? 'sqit.exe' : 'sqit';
      const candidates = [
        path.join(process.env.GOBIN ?? '', exe),
        path.join(process.env.GOPATH ?? '', 'bin', exe),
        path.join(os.homedir(), 'go', 'bin', exe),
      ].filter((c) => path.isAbsolute(c) && fs.existsSync(c));
      this.resolved = onPath(exe) ? 'sqit' : candidates[0] ?? 'sqit';
    }
    return this.resolved;
  }
  private resolved: string | undefined;

  /** Tables listed in .sqit/config.json (no CLI call needed). */
  tables(): string[] {
    if (!this.root) return [];
    try {
      const cfg = JSON.parse(fs.readFileSync(path.join(this.root, '.sqit', 'config.json'), 'utf8'));
      return Array.isArray(cfg.tables) ? cfg.tables : [];
    } catch {
      return [];
    }
  }

  run(args: string[]): Promise<RunResult> {
    return new Promise((resolve) => {
      if (!this.root) {
        resolve({ code: 2, stdout: '', stderr: 'no sqit repository in the workspace' });
        return;
      }
      cp.execFile(
        this.executable,
        args,
        { cwd: this.root, maxBuffer: 64 * 1024 * 1024, windowsHide: true },
        (err, stdout, stderr) => {
          const code = err ? ((err as cp.ExecFileException).code as number | undefined) ?? 1 : 0;
          if (err && typeof (err as NodeJS.ErrnoException).code === 'string') {
            // ENOENT etc.: executable not found
            resolve({ code: 127, stdout: String(stdout ?? ''), stderr: `${(err as Error).message}` });
            return;
          }
          resolve({ code, stdout: String(stdout ?? ''), stderr: String(stderr ?? '') });
        }
      );
    });
  }

  /** Runs a command and echoes everything to the output channel. */
  async runLogged(args: string[], reveal = true): Promise<RunResult> {
    this.output.appendLine(`$ sqit ${args.join(' ')}`);
    const r = await this.run(args);
    if (r.stdout) this.output.append(r.stdout.endsWith('\n') ? r.stdout : r.stdout + '\n');
    if (r.stderr) this.output.append(r.stderr.endsWith('\n') ? r.stderr : r.stderr + '\n');
    this.output.appendLine(`(exit ${r.code})\n`);
    if (reveal) this.output.show(true);
    return r;
  }

  async json<T>(args: string[]): Promise<T> {
    const r = await this.run([...args, '--json']);
    if (r.code === 127) throw new Error(`cannot run '${this.executable}': ${r.stderr.trim()} – set "sqit.executable" in the settings`);
    try {
      return JSON.parse(r.stdout) as T;
    } catch {
      throw new Error((r.stderr || r.stdout || `sqit exited with ${r.code}`).trim());
    }
  }

  status(): Promise<StatusJson> {
    return this.json<StatusJson>(['status']);
  }

  table(name: string, mode: 'working' | 'upstream'): Promise<TableJson> {
    const args = ['table', name];
    if (mode === 'upstream') args.push('--upstream');
    return this.json<TableJson>(args);
  }
}

function onPath(exe: string): boolean {
  const dirs = (process.env.PATH ?? '').split(path.delimiter).filter(Boolean);
  const names = process.platform === 'win32' ? [exe, exe.replace(/\.exe$/i, '')] : [exe];
  return dirs.some((d) => names.some((n) => fs.existsSync(path.join(d, n))));
}

/** Finds the workspace folder (or sub-folder) that contains .sqit/config.json. */
export async function findRepoRoot(): Promise<string | undefined> {
  for (const f of vscode.workspace.workspaceFolders ?? []) {
    if (fs.existsSync(path.join(f.uri.fsPath, '.sqit', 'config.json'))) return f.uri.fsPath;
  }
  const found = await vscode.workspace.findFiles('**/.sqit/config.json', '**/node_modules/**', 1);
  if (found.length > 0) return path.dirname(path.dirname(found[0].fsPath));
  return undefined;
}
