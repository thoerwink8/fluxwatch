// pnpm check：gofmt（有输出算红）、go vet、go test、构建存在的 cmd。
// Linux 上没有托盘界面要的 WebKit，界面包（internal/app、cmd/fluxwatch）只在 mac、Windows 上查；
// 驾驶舱的引擎跑在 Linux 上，靠这一条才能用 pnpm check 验它改的引擎代码。
import { spawnSync } from 'node:child_process';
import { join } from 'node:path';
import { build, hasCmd, root, run } from './lib.mjs';

let ok = true;
const linux = process.platform === 'linux';
const UI_PKGS = ['/internal/app', '/cmd/fluxwatch'];
let pkgs = ['./...'];
if (linux) {
  const list = spawnSync('go', ['list', './...'], { cwd: root, encoding: 'utf8' });
  if (list.status !== 0) {
    console.error(`go list 失败：${list.stderr}`);
    process.exit(1);
  }
  pkgs = list.stdout
    .split('\n')
    .filter((p) => p && !UI_PKGS.some((ui) => p.endsWith(ui) || p.includes(`${ui}/`)));
  console.log('Linux：跳过托盘界面包（internal/app、cmd/fluxwatch），它们在 mac、Windows 的 CI 上查');
}

console.log('== gofmt');
const fmt = spawnSync('gofmt', ['-l', '.'], { cwd: root, encoding: 'utf8' });
if (fmt.error) {
  console.error(`无法执行 gofmt：${fmt.error.message}`);
  ok = false;
} else if (fmt.stdout.trim()) {
  console.error(`以下文件没格式化（跑 gofmt -w 修）：\n${fmt.stdout}`);
  ok = false;
}

console.log('== go vet');
ok = run('go', ['vet', ...pkgs]) && ok;

console.log('== go test');
ok = run('go', ['test', ...pkgs]) && ok;

console.log('== build');
for (const name of linux ? ['fluxwatch-cli'] : ['fluxwatch-cli', 'fluxwatch']) {
  if (!hasCmd(name)) {
    console.log(`跳过 cmd/${name}：还没有代码`);
    continue;
  }
  ok = build(name, join(root, '_tmp', 'check-bin')) !== null && ok;
}

if (!ok) {
  console.error('check 未通过');
  process.exit(1);
}
console.log('check 通过');
