// Package frontend 把面板、发现窗、设置窗的 HTML/CSS/JS 打进二进制。
// 纯静态文件，不走打包工具；Wails 运行时从 /wails/runtime.js 取。
package frontend

import "embed"

// Assets 是 frontend/ 下的静态文件。
//
//go:embed *.html *.css *.js
var Assets embed.FS
