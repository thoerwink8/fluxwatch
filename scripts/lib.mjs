// 脚本共用的小工具。全部用 Node 标准库，mac / Windows 通用。
import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
export const exe = (name) => (process.platform === 'win32' ? `${name}.exe` : name);
export const hasCmd = (name) => existsSync(join(root, 'cmd', name));

// 前台跑一条命令，输出直通；失败返回 false。
export function run(cmd, args, opts = {}) {
  console.log(`$ ${[cmd, ...args].join(' ')}`);
  const r = spawnSync(cmd, args, { cwd: root, stdio: 'inherit', ...opts });
  if (r.error) {
    console.error(`无法执行 ${cmd}：${r.error.message}`);
    return false;
  }
  return r.status === 0;
}

// 构建 ./cmd/<name> 到 outDir。Windows 上托盘应用用 -H windowsgui 不弹控制台。
export function build(name, outDir) {
  const out = join(outDir, exe(name));
  const args = ['build'];
  if (process.platform === 'win32' && name === 'fluxwatch') {
    args.push('-ldflags', '-H windowsgui');
  }
  args.push('-o', out, `./cmd/${name}`);
  return run('go', args) ? out : null;
}
