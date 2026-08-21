// Package web 内嵌控制台的静态页面资源，使最终产物保持为单个可执行文件。
package web

import "embed"

// Assets 是编译进二进制的前端资源。
//
//go:embed dist
var Assets embed.FS
