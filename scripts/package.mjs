// 发版打包。用法：
//   node scripts/package.mjs darwin  <arch> <version> <app-bin> <cli-bin> <outdir>
//   node scripts/package.mjs windows <arch> <version> <app-bin> <cli-bin> <outdir>
// darwin：做 Fluxwatch.app（LSUIElement=true，ad-hoc 签名，带图标）并 zip；cli 一并放进 zip 外的同名包里。
// windows：fluxwatch.exe + fluxwatch-cli.exe 打 zip。
import { spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const BUNDLE_ID = 'io.github.thoerwink8.fluxwatch';
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
  const res = join(stage, 'Fluxwatch.app', 'Contents', 'Resources');
  mkdirSync(res, { recursive: true });
  copyFileSync(join(here, 'assets', 'Fluxwatch.icns'), join(res, 'Fluxwatch.icns'));
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
  <key>CFBundleIconFile</key><string>Fluxwatch</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
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
  // ad-hoc 签名（没做公证：公证要付费开发者账号）。.app 要把 Info.plist 绑进签名，cli 单独签。
  sh('codesign', ['--force', '--deep', '-s', '-', '--identifier', BUNDLE_ID, join(stage, 'Fluxwatch.app')]);
  if (cliBin) sh('codesign', ['--force', '-s', '-', '--identifier', `${BUNDLE_ID}.cli`, join(stage, 'fluxwatch-cli')]);
  sh('codesign', ['--verify', '--strict', '--deep', '--verbose=2', join(stage, 'Fluxwatch.app')]);
  // ditto 保留可执行位和 .app 结构
  sh('ditto', ['-c', '-k', '--norsrc', stage, zip]);
} else if (os === 'windows') {
  copyFileSync(appBin, join(stage, 'fluxwatch.exe'));
  copyFileSync(cliBin, join(stage, 'fluxwatch-cli.exe'));
  // 用 Windows 自带的 bsdtar（System32\tar.exe），-a 按扩展名出 zip。
  // 不能用 PATH 上的 tar：在 Git Bash 里那是 GNU tar，会把 D:\... 当成远程主机。
  const bsdtar = join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'tar.exe');
  sh(bsdtar, ['-a', '-c', '-f', name, '-C', stage, 'fluxwatch.exe', 'fluxwatch-cli.exe'], out);
} else {
  console.error(`不支持的平台：${os}`);
  process.exit(2);
}
rmSync(stage, { recursive: true, force: true });
console.log(`已生成 ${zip}`);
