// 发版打包。用法：
//   node scripts/package.mjs darwin  <arch> <version> <app-bin> <cli-bin> <outdir>
//   node scripts/package.mjs windows <arch> <version> <app-bin> <cli-bin> <outdir>
// darwin：做 Fluxwatch.app（LSUIElement=true，不签名）并 zip；cli 一并放进 zip 外的同名包里。
// windows：fluxwatch.exe + fluxwatch-cli.exe 打 zip。
import { spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const [os, arch, version, appBin, cliBin, outDir] = process.argv.slice(2);
if (!outDir) {
  console.error('用法：package.mjs <darwin|windows> <arch> <version> <app-bin> <cli-bin> <outdir>');
  process.exit(2);
}
const out = resolve(outDir);
const stage = join(out, `stage-${os}-${arch}`);
rmSync(stage, { recursive: true, force: true });
mkdirSync(stage, { recursive: true });
const name = `fluxwatch-${version}-${os}-${arch}.zip`;
const zip = join(out, name);
rmSync(zip, { force: true });

function sh(cmd, args, cwd) {
  const r = spawnSync(cmd, args, { cwd, stdio: 'inherit' });
  if (r.status !== 0) {
    console.error(`${cmd} 失败`);
    process.exit(1);
  }
}

if (os === 'darwin') {
  const macos = join(stage, 'Fluxwatch.app', 'Contents', 'MacOS');
  mkdirSync(macos, { recursive: true });
  copyFileSync(appBin, join(macos, 'fluxwatch'));
  chmodSync(join(macos, 'fluxwatch'), 0o755);
  const v = version.replace(/^v/, '');
  writeFileSync(
    join(stage, 'Fluxwatch.app', 'Contents', 'Info.plist'),
    `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Fluxwatch</string>
  <key>CFBundleDisplayName</key><string>Fluxwatch</string>
  <key>CFBundleIdentifier</key><string>io.github.thoerwink8.fluxwatch</string>
  <key>CFBundleExecutable</key><string>fluxwatch</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${v}</string>
  <key>CFBundleVersion</key><string>${v}</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`,
  );
  if (cliBin) {
    copyFileSync(cliBin, join(stage, 'fluxwatch-cli'));
    chmodSync(join(stage, 'fluxwatch-cli'), 0o755);
  }
  // ditto 保留可执行位和 .app 结构
  sh('ditto', ['-c', '-k', '--norsrc', stage, zip]);
} else if (os === 'windows') {
  copyFileSync(appBin, join(stage, 'fluxwatch.exe'));
  copyFileSync(cliBin, join(stage, 'fluxwatch-cli.exe'));
  // Windows 自带 bsdtar，-a 按扩展名出 zip
  sh('tar', ['-a', '-c', '-f', zip, '-C', stage, 'fluxwatch.exe', 'fluxwatch-cli.exe']);
} else {
  console.error(`不支持的平台：${os}`);
  process.exit(2);
}
rmSync(stage, { recursive: true, force: true });
console.log(`已生成 ${zip}`);
