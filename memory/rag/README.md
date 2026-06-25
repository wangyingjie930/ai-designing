# RAG Ingestion Pipeline

这个目录实现 payroll policy 的 RAG ingestion 和发布流程：读取 `manifest.yaml`，复用仓库根目录 `.env`，用 LlamaIndex 解析 PDF，执行 `llama_index.core.ingestion.IngestionPipeline` 完成 docstore 增量判断、chunk 和 embedding，再构建 dense index 和 BM25 index。

当前架构约束是：

- `raw_docs` 普通变更不会改变 collection 名称，会直接更新 alias 当前指向的线上 collection。
- `parser` / `chunker` / `embedding` 契约变更会生成新的 collection 名称，需要重新跑 ingestion 生成新索引文件。
- 新 collection 必须通过 golden questions 数据集测试；通过后 `release.py` 默认切换 alias。
- `manifest.yaml` 只放人工输入和索引契约，`source_count`、`chunk_count`、`corpus_revision`、`golden_set_passed`、alias 状态等运行结果全部由命令生成。

## 目录约定

- `manifest.yaml`: 输入文档、collection 前缀和索引契约。
- `raw_docs/`: 放原始 PDF，例如 `raw_docs/payroll-bonus-policy-2026-v2.pdf`。
- `ingest.py`: ingestion 入口，按 manifest 生成或更新真实 collection。
- `config.py`: `.env` 和本地参数读取。
- `manifest.py`: manifest 校验、collection 名生成、raw doc 路径解析、运行产物 manifest 写入。
- `pipeline.py`: LlamaIndex `IngestionPipeline.run(...)` 主流程。
- `release.py`: 对 manifest 对应 collection 跑 golden questions，通过后切换 alias。
- `alias.py`: alias 状态查看、解析和手动切换工具。
- `app_query.py`: 模拟业务侧通过 alias 查询线上 collection。

## Collection 与 Alias

`manifest.yaml` 不再人工声明完整 collection 名，只声明前缀：

```yaml
index_manifest:
  corpus_name: payroll-policy
  collection_prefix: payroll_rag
```

实际 collection 名由代码生成：

```text
<collection_prefix>_<pipeline_signature>
```

`pipeline_signature` 是 parser、chunker、embedding 的 `version + params` 稳定 hash，例如：

```text
payroll_rag_1406187972c4
```

alias 默认由前缀派生：

```text
<collection_prefix>_current
```

例如 `payroll_rag_current`。业务查询只依赖 alias；alias 文件保存在：

```text
output/rag/aliases/payroll_rag_current.yaml
```

这保证了两件事：

- raw doc 内容或列表变更只改变 `corpus_revision`，不改变 collection 名。
- parser/chunker/embedding 任一契约变更都会改变 `pipeline_signature`，从而写入新的 collection 目录。

## Manifest 边界

`manifest.yaml` 只描述“要把哪些文档按什么索引契约写入哪个 collection 前缀”：

```yaml
raw_docs:
  - payroll-bonus-policy-2026-v2.pdf

index_manifest:
  corpus_name: payroll-policy
  collection_prefix: payroll_rag
  components:
    parser: pdf-parser-2.4
    chunker: clause-aware-v2-1
    embedding: gemini-embedding-001-1536
    hybrid_index: bm25-cn-v1+dense-v3

parser:
  version: pdf-parser-2.4
  params:
    engine: llamaindex-pdfreader
    return_full_document: false
    ocr_enabled: false
    table_mode: plain_text
    page_split: true

chunker:
  version: clause-aware-v2-1
  params:
    splitter: llamaindex-sentence-splitter
    chunk_size: 768
    chunk_overlap: 100
    separator: "\n"
    secondary_chunking_regex: "[^。！？；;.!?\\n]+[。！？；;.!?]?|[\\n]+"
    include_metadata: true
    include_prev_next_rel: true

embedding:
  version: gemini-embedding-001-1536
  params:
    model: google:gemini-embedding-001
    dim: 1536
    output_dimensionality: 1536
    embed_batch_size: 32
    timeout_seconds: 60.0
    max_retries: 5
```

不要在 `manifest.yaml` 里写这些字段：

```yaml
collection: ...
source_count: ...
chunk_count: ...
corpus_revision: ...
golden_set_passed: ...
alias_after_release: ...
```

这些都是运行事实或发布质量结果，必须由命令执行后生成。

## 增量更新

raw docs 的普通增量更新直接写 alias 当前指向的线上 collection。流程是：

1. 保持 parser/chunker/embedding 不变。
2. 修改 `raw_docs` 或替换 `raw_docs/` 下的文件。
3. 执行 `ingest.py`。

因为 collection 名只由 parser/chunker/embedding 决定，所以 raw doc 变化会复用同一个目录：

```text
output/rag/payroll_rag_<pipeline_signature>/docstore/docstore.json
output/rag/payroll_rag_<pipeline_signature>/docstore/document-hashes.yaml
```

每个 PDF page 会被赋予稳定 doc_id：

```text
<collection>::<source_file>::page-<page_label>
```

`IngestionPipeline` 以 `DocstoreStrategy.UPSERTS_AND_DELETE` 运行，并同时接入持久化的 `SimpleDocumentStore` 和 `SimpleVectorStore`。第二次执行时，docstore 会用 `doc_id -> document_hash` 判断文档是否变化：hash 没变则跳过 chunk + embedding，hash 变了才删除旧 ref doc 对应向量并重处理。

命令输出里的 `processed_source_count` / `processed_chunk_count` 表示本次实际重处理数量；`actual_source_count` / `actual_chunk_count` 表示当前索引总量。这些值只出现在命令输出、`collection-state.yaml` 和 `release-manifest.yaml`，不回写到 `manifest.yaml`。

## 全量更新

parser/chunker/embedding 变更属于全量更新。流程是：

1. 修改 `manifest.yaml` 中对应组件的 `version` 或 `params`。
2. 执行 `ingest.py`，生成新的 `output/rag/<collection_prefix>_<pipeline_signature>/`。
3. 执行 `release.py` 跑 golden questions 数据集测试。
4. 测试通过后，`release.py` 默认把 alias 切到新 collection；测试失败时 alias 保持原线上 collection 不变。

如需只跑评测、不切 alias：

```bash
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/release.py --no-switch-alias
```

也可以手动切换已通过评测的 collection：

```bash
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/alias.py switch --manifest manifest.yaml
```

## 安装依赖

建议用临时虚拟环境，不污染 Go 仓库：

```bash
cd memory/rag
python3 -m venv /private/tmp/ai-designing-rag-venv
/private/tmp/ai-designing-rag-venv/bin/python -m pip install -r requirements.txt
```

## 执行

先校验 manifest、配置和原始 PDF 路径：

```bash
cd /Users/wangyingjie/Documents/code/ai-designing
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/ingest.py --dry-run
```

构建或更新真实索引：

```bash
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/ingest.py
```

连续执行第二次，如果 raw doc 没变，应该看到：

```text
processed_source_count: 0
processed_chunk_count: 0
changed_document_ids: []
```

## .env 配置

默认读取仓库根目录 `.env`。当前实现会优先读 RAG 专用配置；Gemini embedContent 会优先使用 `GOOGLE_API_KEY`：

```bash
RAG_EMBEDDING_API_KEY=...
RAG_EMBEDDING_BASE_URL=https://generativelanguage.googleapis.com/v1beta
RAG_EMBEDDING_ENDPOINT_PATH=models/gemini-embedding-001:embedContent
RAG_EMBEDDING_MODEL=google:gemini-embedding-001
RAG_EMBEDDING_DIM=1536
```

如果没有 RAG 专用键，会退回：

```bash
EMBEDDING_API_KEY / GOOGLE_API_KEY / OPENAI_API_KEY
EMBEDDING_BASE_URL / LLM_OPENAI_BASE_URL
EMBEDDING_ENDPOINT_PATH
EMBEDDING_MODEL / LLM_MODEL
```

`index_manifest.components` 只负责关联当前索引选用的组件 version；`parser`、`chunker`、`embedding`、`hybrid_index` 顶层块负责定义对应 version 的参数。parser/chunker/embedding 的 version 或 params 变更会生成新的 collection 名。preflight 只校验当前代码是否能执行这些参数，例如 parser engine、chunker splitter、正则合法性和 embedding 运行配置。`embedding.params.model` 和 `embedding.params.dim` 会做严格校验：manifest 里的值必须等于本次 `.env` 解析出的真实 embedding 配置。旧的 `parser_version` / `chunker_version` / `hybrid_index_version` flat 字段，以及旧的 `index_manifest.collection` 字段都会被拒绝。

可选参数：

```bash
RAG_OUTPUT_DIR=output/rag
RAG_BM25_TOP_K=12
RAG_BM25_LANGUAGE=zh
```

chunker 的 `chunk_size` / `chunk_overlap` 来自 `manifest.yaml`，不是 `.env`。

## 输出

默认输出到：

```text
output/rag/
├── aliases/
│   └── payroll_rag_current.yaml
└── payroll_rag_<pipeline_signature>/
    ├── dense/
    ├── bm25/
    ├── docstore/
    ├── collection-state.yaml
    ├── golden-report.yaml
    ├── quality-gate.yaml
    └── release-manifest.yaml
```

`release-manifest.yaml` 会记录 pipeline 自动生成的运行事实：

```yaml
collection: payroll_rag_1406187972c4
collection_alias: payroll_rag_current
pipeline_signature: 1406187972c4
build:
  corpus_revision: sha256:...
  actual_source_count: 5
  actual_chunk_count: 5
  processed_source_count: 5
  processed_chunk_count: 5
quality_gate:
  golden_set_passed: null
  golden_report: null
```

`release.py` 跑完后会把 `quality_gate.golden_set_passed` 回写为真实结果。

## 质量门禁和 Alias 切换

对 manifest 对应 collection 跑 golden questions：

```bash
cd /Users/wangyingjie/Documents/code/ai-designing
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/release.py --manifest manifest.yaml
```

通过或失败都会写：

```text
output/rag/payroll_rag_<pipeline_signature>/golden-report.yaml
output/rag/payroll_rag_<pipeline_signature>/quality-gate.yaml
```

通过时还会写：

```text
output/rag/aliases/payroll_rag_current.yaml
```

查看 alias：

```bash
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/alias.py status --alias payroll_rag_current
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/alias.py resolve --alias payroll_rag_current
```

## 业务查询

模拟业务侧查询，默认通过 alias 解析真实 collection：

```bash
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/app_query.py \
  --alias payroll_rag_current \
  --role employee \
  --question "Which employees are eligible for the 2026 bonus payout?"
```

调试时可以绕过 alias，直接指定真实 collection：

```bash
/private/tmp/ai-designing-rag-venv/bin/python memory/rag/app_query.py \
  --collection payroll_rag_1406187972c4 \
  --role employee \
  --question "Which employees are eligible for the 2026 bonus payout?"
```
