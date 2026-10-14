// pnpm check：gofmt（有输出算红）、go vet、go test、构建存在的 cmd。
import { spawnSync } from 'node:child_process';
import { join } from 'node:path';
import { build, hasCmd, root, run } from './lib.mjs';

let ok = true;

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
ok = run('go', ['vet', './...']) && ok;

console.log('== go test');
ok = run('go', ['test', './...']) && ok;

console.log('== build');
for (const name of ['fluxwatch-cli', 'fluxwatch']) {
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
