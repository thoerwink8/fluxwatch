// pnpm start：构建托盘应用到 bin/，后台启动，pid 写到 _tmp/fluxwatch.pid。
import { spawn } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { build, hasCmd, root } from './lib.mjs';

export const pidFile = join(root, '_tmp', 'fluxwatch.pid');

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (e) {
    return e.code === 'EPERM';
  }
}

if (!hasCmd('fluxwatch')) {
  console.error('托盘应用还没有代码（#2）：cmd/fluxwatch 不存在，无法启动。');
  process.exit(1);
}

try {
  const old = Number(readFileSync(pidFile, 'utf8'));
  if (old && alive(old)) {
    console.error(`已在运行（pid ${old}）。先 pnpm stop。`);
    process.exit(1);
  }
} catch {}

const bin = build('fluxwatch', join(root, 'bin'));
if (!bin) {
  console.error('构建失败');
  process.exit(1);
}

// 去掉 TZ：从 AI 会话里起时会继承会话的时区（实测 America/New_York），应用该跟系统时区走。
const env = { ...process.env };
delete env.TZ;
const child = spawn(bin, [], { cwd: root, env, detached: true, stdio: 'ignore', windowsHide: true });
child.on('error', (e) => {
  console.error(`启动失败：${e.message}`);
  process.exit(1);
});
child.unref();
mkdirSync(join(root, '_tmp'), { recursive: true });
writeFileSync(pidFile, String(child.pid));
console.log(`已启动 fluxwatch（pid ${child.pid}），pid 文件：_tmp/fluxwatch.pid`);
