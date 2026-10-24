# 安全策略 / Security Policy

English summary: report vulnerabilities privately via GitHub's **Report a vulnerability** button on the Security tab of this repository. Please do not open public issues for security problems.

## 支持版本

个人维护项目，只修复最新正式 Release 与其对应的 main 代码；历史版本不再维护，升级请见 [Releases](https://github.com/hwj123hwj/easyagent/releases)。

## 私下上报

1. 打开仓库的 **Security** 标签页，点击 **Report a vulnerability**，按模板描述问题。
2. 内容尽量包含：影响的组件（核心 CLI / 服务端 / 桌面客户端 / Android / 飞书桥接 / SDK）、版本号或 commit、复现步骤、影响评估。日志与复现材料中的密钥请先脱敏。
3. 不要在公开 issue、PR、讨论或截图里包含可利用细节或凭据。

## 响应

- 目标 **7 天内**确认收到的报告；修复按严重程度排期，小修复按发布节奏攒批，高危问题单独发补丁版本。
- 修复发布前不公开细节；发布后在 Release 说明中致谢报告者（除非要求匿名）。

## 范围

- 接受：本仓库产品代码、发布与 CI 脚本、SDK 中的安全问题；越权访问、路径穿越、凭据泄露、命令注入、供应链问题等。
- 不接受：针对自己部署环境的配置错误、未启用认证导致的服务暴露（属部署责任，见 [安全模型](docs/SECURITY_MODEL.md)）、DoS 泛泛报告、SDK 使用方式问题。
