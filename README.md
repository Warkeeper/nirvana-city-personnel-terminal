# 涅槃城人事部管理终端

这是一个 Go + SQLite 的离线单机应用。运行时只需要编译出的二进制文件和同级 `data` 目录中的 SQLite 数据，不需要 Node/npm、Java、Maven、Python、SQLite DLL 或外部静态资源目录。

## 运行

```powershell
go run ./cmd/ncpt
```

程序会监听 `127.0.0.1` 的随机端口，并自动打开浏览器。默认数据目录在二进制同级：

- `data/ncfms.db`
- `data/backups`
- `data/ncfms.lock`
- `data/ncfms.url`

可通过参数指定数据目录：

```powershell
ncpt.exe -data-dir D:\ncfms-data
```

同一个 `data-dir` 只允许一个实例运行。第二个实例会读取已运行实例的 URL，打开浏览器后退出。

## Excel 合并导入

离线维护时可以用当前 Go 版本导出的 7-Sheet xlsx 合并导入本地 SQLite：

```powershell
ncpt.exe --merge .\data.xlsx
ncpt.exe --data-dir D:\ncfms-data --merge .\data.xlsx
```

merge 模式不会启动 HTTP 服务，也不会打开浏览器。它会复用单实例锁；如果同一个 `data-dir` 正在运行应用，命令会失败并提示先关闭应用。

只支持当前导出的 7 个 Sheet：`数据库`、`金条流水`、`玩家进出城记录`、`身份历史`、`时长增加记录`、`开城记录`、`已取消进出城记录`。旧 3-Sheet 文件会被拒绝，并列出缺失 Sheet。

导入前会先创建 `data/backups/ncfms-merge-*.db` 备份；备份失败会阻止导入。整个合并在一个 SQLite 事务内完成，任何行解析、类型冲突、缺字段或编号格式错误都会回滚全部变更。

居民编号列必须是文本单元格，导入时只 trim 首尾空白；数字型编号单元格会直接报错，避免丢失前导零。`数据库` Sheet 的 `金条余额` 是最终余额，导入金条流水只补历史记录，不重新计算余额。

## 构建

主包路径是 `./cmd/ncpt`。

```powershell
$env:CGO_ENABLED='0'
$env:GOOS='windows'; $env:GOARCH='amd64'; go build ./cmd/ncpt
$env:GOOS='darwin';  $env:GOARCH='amd64'; go build ./cmd/ncpt
$env:GOOS='darwin';  $env:GOARCH='arm64'; go build ./cmd/ncpt
```

SQLite 驱动使用 `modernc.org/sqlite`，支持 `CGO_ENABLED=0`。

## 数据和备份

SQLite 是唯一业务事实来源。前端所有业务读写都走本机 REST API，写操作在 SQLite 事务提交成功后才返回成功。

数据库启用：

- WAL
- foreign_keys
- synchronous FULL
- busy_timeout

开城前和 migration 前会自动做 SQLite 一致性备份，备份文件放在 `data/backups`，保留最近 20 份。备份失败会阻止开城或 migration。

手工恢复方式：

1. 退出应用。
2. 备份当前 `data/ncfms.db`。
3. 从 `data/backups` 选择要恢复的 `ncfms-*.db`。
4. 将该备份复制为 `data/ncfms.db`。
5. 重新启动应用。

## 当前限制

- 云同步暂缓重构，按钮只提示后续版本处理，不访问远程接口。
- 浏览器端 Excel 导入已移除；只保留从 SQLite 全量导出 xlsx，以及离线 CLI `--merge` 合并当前 7-Sheet 导出文件。
- macOS 构建产物未签名，首次运行可能需要用户手动允许。
