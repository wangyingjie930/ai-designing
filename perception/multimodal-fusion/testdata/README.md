# multimodal-fusion testdata

这些素材用于本地测试多模态融合链路，PDF 只作为占位输入，默认 `FakePDFExtractor` 不会读取或解析 PDF 内容。

- `reports/fake-market-report.pdf`: 可打开的假 PDF，占位触发 `kind=pdf`。
- `images/market-trend.png`: 趋势图样例，模拟需要视觉模型判断走势的关键图。
- `images/segment-bar.png`: 柱状图样例，模拟客群结构对比。
- `images/process-diagram.png`: 流程图样例，适合单独作为 `kind=image` 测试。
- `images/decorative-logo.png`: 装饰图样例，fake PDF 抽取会标记为不保留。
- `text/report-notes.md`: Markdown 正文样例。
- `text/customer-notes.txt`: 普通文本样例。
- `tables/channel_metrics.csv`: 表格样例。
- `logs/runtime.log`: 日志样例。

重新生成图片和假 PDF:

```bash
go run ./perception/multimodal-fusion/testdata/generate_assets.go
```
