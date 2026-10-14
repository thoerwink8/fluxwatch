// pnpm stop：读 _tmp/fluxwatch.pid 停掉托盘应用。
import { spawnSync } from 'node:child_process';
import { readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { root } from './lib.mjs';

const pidFile = join(root, '_tmp', 'fluxwatch.pid');
let pid;
try {
  pid = Number(readFileSync(pidFile, 'utf8').trim());
} catch {
  console.log('没有在运行（找不到 _tmp/fluxwatch.pid）。');
  process.exit(0);
}
if (!Number.isInteger(pid) || pid <= 0) {
  console.error(`pid 文件内容无效：${pid}`);
  rmSync(pidFile, { force: true });
  process.exit(1);
}

let ok = true;
if (process.platform === 'win32') {
  const r = spawnSync('taskkill', ['/PID', String(pid), '/T', '/F'], { stdio: 'inherit' });
  ok = r.status === 0;
} else {
  try {
    process.kill(pid, 'SIGTERM');
  } catch (e) {
    ok = e.code === 'ESRCH'; // 已经退出算成功
  }
}
rmSync(pidFile, { force: true });
if (!ok) {
  console.error(`停止 pid ${pid} 失败`);
  process.exit(1);
}
console.log(`已停止 pid ${pid}`);
