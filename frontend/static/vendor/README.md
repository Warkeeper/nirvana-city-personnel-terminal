# Offline vendor assets (manual copy)

为了支持离线运行，`src/main/resources/index.html` 已改为引用本地资源，请在发版前手动拷贝以下文件：

1. `./static/vendor/vue/dist/vue.js`
2. `./static/vendor/element-ui/lib/index.js`
3. `./static/vendor/element-ui/lib/theme-chalk/index.css`
4. `./static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.woff`
5. `./static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.ttf`
6. `./static/vendor/xlsx/dist/xlsx.full.min.js`

页面启动时会执行依赖检查；若缺少上述文件，会在页面中展示错误提示。
