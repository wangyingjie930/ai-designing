import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Sequence

import yaml


# 旧版 flat 字段不再兼容；索引组件必须使用顶层 {version, params} 结构。
LEGACY_INDEX_KEYS = {
    "parser_version",
    "parser_params",
    "chunker_version",
    "chunker_params",
    "embedding_model",
    "embedding_dim",
    "embedding_params",
    "hybrid_index_version",
    "hybrid_index_params",
}
TOP_LEVEL_COMPONENT_KEYS = {"parser", "chunker", "embedding", "hybrid_index"}
PIPELINE_COLLECTION_COMPONENT_KEYS = ("parser", "chunker", "embedding")
COLLECTION_SIGNATURE_LENGTH = 12
COLLECTION_SAFE_CHARS = re.compile(r"[^A-Za-z0-9_-]+")


# 表示一个可版本化索引组件；version 选实现契约，params 固定该版本的参数。
@dataclass(frozen=True)
class VersionedComponent:
    version: str
    params: Dict[str, Any]


# 表示 manifest.index_manifest 中人工声明的 collection 前缀和组件引用。
@dataclass(frozen=True)
class IndexManifest:
    corpus_name: str
    collection_prefix: str
    components: Dict[str, str]


# 表示完整 ingestion manifest，包含原始文档、collection 前缀和顶层版本化组件。
@dataclass(frozen=True)
class RAGManifest:
    path: Path
    raw_docs: List[str]
    index_manifest: IndexManifest
    parser: VersionedComponent
    chunker: VersionedComponent
    embedding: VersionedComponent
    hybrid_index: VersionedComponent

    @property
    def parser_version(self) -> str:
        return self.parser.version

    @property
    def chunker_version(self) -> str:
        return self.chunker.version

    @property
    def embedding_model(self) -> str:
        return _required_param_str("embedding", self.embedding.params, "model")

    @property
    def embedding_dim(self) -> int:
        return _required_param_int("embedding", self.embedding.params, "dim")

    @property
    def hybrid_index_version(self) -> str:
        return self.hybrid_index.version

    @property
    def collection(self) -> str:
        return build_collection_name(self)

    @property
    def collection_alias(self) -> str:
        return build_collection_alias(self)

    @property
    def pipeline_signature(self) -> str:
        return build_pipeline_signature(self)


# 从 YAML 文件加载 manifest，并做必需字段校验。
def load_manifest(path: Path) -> RAGManifest:
    with path.open("r", encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}

    raw_docs = data.get("raw_docs")
    index_data = data.get("index_manifest")
    if not isinstance(raw_docs, list) or not raw_docs:
        raise ValueError("manifest.raw_docs must be a non-empty list")
    if not isinstance(index_data, dict):
        raise ValueError("manifest.index_manifest must be a mapping")
    legacy_keys = sorted(LEGACY_INDEX_KEYS & set(index_data))
    if legacy_keys:
        raise ValueError(
            "manifest.index_manifest uses legacy flat keys: %s; use top-level parser/chunker/embedding/hybrid_index {version, params}"
            % ", ".join(legacy_keys)
        )
    nested_components = sorted(TOP_LEVEL_COMPONENT_KEYS & set(index_data))
    if nested_components:
        raise ValueError(
            "manifest.index_manifest must not contain components: %s; move them to top-level"
            % ", ".join(nested_components)
        )
    if "collection" in index_data:
        raise ValueError("manifest.index_manifest.collection is deprecated; use manifest.index_manifest.collection_prefix")
    components = _required_components_ref(index_data)

    index_manifest = IndexManifest(
        corpus_name=_required_str(index_data, "corpus_name", "manifest.index_manifest"),
        collection_prefix=_required_str(index_data, "collection_prefix", "manifest.index_manifest"),
        components=components,
    )
    manifest = RAGManifest(
        path=path,
        raw_docs=[str(item) for item in raw_docs],
        index_manifest=index_manifest,
        parser=_required_component(data, "parser"),
        chunker=_required_component(data, "chunker"),
        embedding=_required_component(data, "embedding"),
        hybrid_index=_required_component(data, "hybrid_index"),
    )
    _validate_component_refs(manifest)
    return manifest


# 读取 index_manifest.components，声明当前索引实际选用的各组件版本。
def _required_components_ref(index_data: Dict[str, Any]) -> Dict[str, str]:
    value = index_data.get("components")
    if not isinstance(value, dict):
        raise ValueError("manifest.index_manifest.components must be a mapping")
    refs = {}
    for key in sorted(TOP_LEVEL_COMPONENT_KEYS):
        refs[key] = _required_str(value, key, "manifest.index_manifest.components")
    return refs


# 校验 version 引用和顶层组件定义一致，避免 index_manifest 和组件参数漂移。
def _validate_component_refs(manifest: RAGManifest) -> None:
    expected = {
        "parser": manifest.parser.version,
        "chunker": manifest.chunker.version,
        "embedding": manifest.embedding.version,
        "hybrid_index": manifest.hybrid_index.version,
    }
    mismatches = [
        "components.%s=%s expected %s" % (key, manifest.index_manifest.components.get(key), version)
        for key, version in expected.items()
        if manifest.index_manifest.components.get(key) != version
    ]
    if mismatches:
        raise ValueError("manifest index/component version mismatch: " + "; ".join(mismatches))


# 读取必需组件字段，强制每个组件都声明 version 和 params。
def _required_component(data: Dict[str, Any], key: str) -> VersionedComponent:
    value = data.get(key)
    path = "manifest.%s" % key
    if not isinstance(value, dict):
        raise ValueError("%s must be a mapping with version and params" % path)
    params = value.get("params")
    if not isinstance(params, dict):
        raise ValueError("%s.params must be a mapping" % path)
    return VersionedComponent(
        version=_required_str(value, "version", path),
        params=dict(params),
    )


# 读取必需字符串字段，缺失时给出可定位的 manifest 错误。
def _required_str(data: Dict[str, Any], key: str, path: str) -> str:
    value = data.get(key)
    if value is None or str(value).strip() == "":
        raise ValueError("%s.%s is required" % (path, key))
    return str(value)


# 读取必需整数字段，避免计数和维度以字符串形式悄悄传递。
def _required_int(data: Dict[str, Any], key: str, path: str) -> int:
    value = data.get(key)
    if value is None:
        raise ValueError("%s.%s is required" % (path, key))
    return int(value)


# 读取组件 params 中的必需字符串字段。
def _required_param_str(component: str, params: Dict[str, Any], key: str) -> str:
    return _required_str(params, key, "manifest.%s.params" % component)


# 读取组件 params 中的必需整数字段。
def _required_param_int(component: str, params: Dict[str, Any], key: str) -> int:
    return _required_int(params, key, "manifest.%s.params" % component)


# 按约定解析 raw_docs；优先支持 manifest 同目录，其次支持 raw_docs 子目录。
def resolve_raw_doc_paths(manifest: RAGManifest) -> List[Path]:
    resolved = []
    base_dir = manifest.path.parent
    for raw_doc in manifest.raw_docs:
        raw_path = Path(raw_doc)
        candidates = [raw_path] if raw_path.is_absolute() else [
            base_dir / raw_path,
            base_dir / "raw_docs" / raw_path,
        ]
        matched = next((candidate for candidate in candidates if candidate.exists()), None)
        if matched is None:
            raise FileNotFoundError("raw doc not found: %s (checked: %s)" % (
                raw_doc,
                ", ".join(str(candidate) for candidate in candidates),
            ))
        resolved.append(matched)
    return resolved


# 计算原始文档内容 hash，供 pipeline 生成 corpus_revision 和审计快照。
def build_raw_doc_fingerprints(raw_doc_paths: Sequence[Path]) -> List[Dict[str, Any]]:
    fingerprints = []
    for path in raw_doc_paths:
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        fingerprints.append({
            "source_file": path.name,
            "path": str(path),
            "sha256": digest,
            "size_bytes": path.stat().st_size,
        })
    return fingerprints


# 根据原始文档和索引契约生成内容修订号，避免人工在 manifest 里维护运行结果。
def build_corpus_revision(manifest: RAGManifest, raw_doc_fingerprints: Sequence[Dict[str, Any]]) -> str:
    payload = {
        "raw_docs": [
            {
                "source_file": item["source_file"],
                "sha256": item["sha256"],
            }
            for item in raw_doc_fingerprints
        ],
        "index_contract": manifest_contract_to_dict(manifest),
    }
    encoded = json.dumps(payload, ensure_ascii=False, sort_keys=True).encode("utf-8")
    return "sha256:%s" % hashlib.sha256(encoded).hexdigest()[:16]


# 只用 parser/chunker/embedding 契约生成 collection 指纹，raw_docs 变化仍复用线上 collection。
def build_pipeline_signature(manifest: RAGManifest) -> str:
    payload = {
        key: component_to_dict(getattr(manifest, key))
        for key in PIPELINE_COLLECTION_COMPONENT_KEYS
    }
    encoded = json.dumps(payload, ensure_ascii=False, sort_keys=True).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()[:COLLECTION_SIGNATURE_LENGTH]


# 实际 collection 名由前缀和 pipeline 指纹组成，组件契约变化时自然写入新目录。
def build_collection_name(manifest: RAGManifest) -> str:
    return "%s_%s" % (
        normalize_collection_prefix(manifest.index_manifest.collection_prefix),
        manifest.pipeline_signature,
    )


# 线上稳定入口使用 alias；默认按 collection 前缀派生，避免在 manifest 再维护一个当前名。
def build_collection_alias(manifest: RAGManifest) -> str:
    return "%s_current" % normalize_collection_prefix(manifest.index_manifest.collection_prefix)


# 将 prefix 约束到可作为目录名和 alias 文件名的安全字符集合。
def normalize_collection_prefix(prefix: str) -> str:
    normalized = COLLECTION_SAFE_CHARS.sub("_", prefix.strip()).strip("_")
    if not normalized:
        raise ValueError("manifest.index_manifest.collection_prefix must contain a valid collection name prefix")
    return normalized


# 将完整索引契约转成稳定 YAML 字段，便于产物 manifest 可审阅。
def manifest_contract_to_dict(manifest: RAGManifest) -> Dict[str, Any]:
    return {
        "index_manifest": index_manifest_to_dict(manifest.index_manifest),
        "parser": component_to_dict(manifest.parser),
        "chunker": component_to_dict(manifest.chunker),
        "embedding": component_to_dict(manifest.embedding),
        "hybrid_index": component_to_dict(manifest.hybrid_index),
    }


# 将 collection 身份信息转成稳定 YAML 字段。
def index_manifest_to_dict(index_manifest: IndexManifest) -> Dict[str, Any]:
    return {
        "corpus_name": index_manifest.corpus_name,
        "collection_prefix": index_manifest.collection_prefix,
        "components": dict(index_manifest.components),
    }


# 将单个版本化组件转成稳定 YAML 字段。
def component_to_dict(component: VersionedComponent) -> Dict[str, Any]:
    return {
        "version": component.version,
        "params": dict(component.params),
    }


# 构建实际落盘的 release manifest，保留输入契约并补充 pipeline 自动生成的运行事实。
def build_release_manifest(
    manifest: RAGManifest,
    raw_doc_paths: Sequence[Path],
    raw_doc_fingerprints: Sequence[Dict[str, Any]],
    corpus_revision: str,
    dense_index_dir: Path,
    bm25_index_dir: Path,
    docstore_path: Path,
    docstore_hashes_path: Path,
    vector_store_path: Path,
    collection_state_path: Path,
    actual_source_count: int,
    actual_chunk_count: int,
    processed_source_count: int,
    processed_chunk_count: int,
    changed_document_ids: Sequence[str],
    actual_embedding_model: str,
    actual_embedding_dim: int,
    built_at: str,
) -> Dict[str, Any]:
    return {
        "collection": manifest.collection,
        "collection_alias": manifest.collection_alias,
        "pipeline_signature": manifest.pipeline_signature,
        "pipeline_signature_components": list(PIPELINE_COLLECTION_COMPONENT_KEYS),
        "raw_docs": [str(path) for path in raw_doc_paths],
        "raw_doc_fingerprints": list(raw_doc_fingerprints),
        **manifest_contract_to_dict(manifest),
        "build": {
            "built_at": built_at,
            "corpus_revision": corpus_revision,
            "actual_source_count": actual_source_count,
            "actual_chunk_count": actual_chunk_count,
            "processed_source_count": processed_source_count,
            "processed_chunk_count": processed_chunk_count,
            "actual_embedding_model": actual_embedding_model,
            "actual_embedding_dim": actual_embedding_dim,
            "dense_index_dir": str(dense_index_dir),
            "bm25_index_dir": str(bm25_index_dir),
            "docstore_path": str(docstore_path),
            "docstore_hashes_path": str(docstore_hashes_path),
            "vector_store_path": str(vector_store_path),
            "collection_state_path": str(collection_state_path),
            "embedding_model_matched": actual_embedding_model == manifest.embedding_model,
            "embedding_dim_matched": actual_embedding_dim == manifest.embedding_dim,
        },
        "quality_gate": {
            "golden_set_passed": None,
            "golden_report": None,
            "evaluated_at": None,
            "note": "golden_set_passed 由 release.py 跑完 golden questions 后生成。",
        },
        "docstore_change_detection": {
            "strategy": "DocstoreStrategy.UPSERTS_AND_DELETE",
            "hash_mapping": "doc_id -> document_hash",
            "changed_document_count": len(changed_document_ids),
            "unchanged_document_count": actual_source_count - len(changed_document_ids),
            "changed_document_ids": list(changed_document_ids),
            "document_hash_snapshot_path": str(docstore_hashes_path),
        },
    }


# 写 YAML 文件时保持字段顺序，方便人工 diff 发布产物。
def write_yaml(path: Path, data: Dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as f:
        yaml.safe_dump(data, f, allow_unicode=True, sort_keys=False)
