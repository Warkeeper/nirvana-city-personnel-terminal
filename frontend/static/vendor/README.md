# Offline vendor assets

为了支持离线运行，`frontend/index.html` 引用本地 Vue2 和 Element UI 资源；发布时这些文件会被 Go `embed` 打进二进制，不需要在运行目录额外放置静态资源目录。

1. `./static/vendor/vue/dist/vue.js`
2. `./static/vendor/element-ui/lib/index.js`
3. `./static/vendor/element-ui/lib/theme-chalk/index.css`
4. `./static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.woff`
5. `./static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.ttf`

Excel 导出由 Go 后端从 SQLite 生成完整 xlsx；前端不再加载浏览器 XLSX，也不再支持 Excel 导入。
