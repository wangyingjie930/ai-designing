from dataclasses import dataclass
from pathlib import Path
from typing import Mapping, Optional


# 表示真实 embedding 服务的运行配置，模型名必须来自 .env/环境变量。
@dataclass(frozen=True)
class EmbedderConfig:
    api_key: Optional[str]
    base_url: Optional[str]
    endpoint_path: Optional[str]
    model: str
    dim: int
    batch_size: int
    timeout_seconds: float
    max_retries: int


# 表示一次 ingestion 的本地目录和切块参数配置。
@dataclass(frozen=True)
class PipelineConfig:
    repo_root: Path
    rag_dir: Path
    output_dir: Path
    embedder: EmbedderConfig
    bm25_top_k: int
    bm25_language: str


# 表示配置缺失或非法，直接阻止 pipeline 进入半真实状态。
class ConfigError(ValueError):
    pass


# 从多个环境变量候选中读取第一个非空值，用于兼容仓库已有 .env。
def first_env(env: Mapping[str, str], *keys: str) -> Optional[str]:
    for key in keys:
        value = env.get(key)
        if value:
            return value
    return None


# 将环境变量解析为 int，避免 CLI 主流程散落类型转换逻辑。
def env_int(env: Mapping[str, str], key: str, default: int) -> int:
    value = env.get(key)
    if not value:
        return default
    return int(value)


# 将环境变量解析为 float，集中处理 timeout 这类数值配置。
def env_float(env: Mapping[str, str], key: str, default: float) -> float:
    value = env.get(key)
    if not value:
        return default
    return float(value)


# 从当前文件位置推导仓库根目录，保证在 memory/rag 下直接执行也能复用根目录 .env。
def default_repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


# 只解析 RAG 输出目录，供查询/运维命令在不加载 embedding 密钥时定位 collection。
def resolve_output_dir(env_file: Optional[Path]) -> Path:
    import os

    repo_root = default_repo_root()
    resolved_env = env_file or repo_root / ".env"
    if resolved_env.exists():
        load_env_file(resolved_env)
    return Path(first_env(os.environ, "RAG_OUTPUT_DIR") or repo_root / "output" / "rag")


# 宽松加载 .env，只处理 KEY=VALUE；文件值覆盖当前 shell，保证本次 ingestion 以 .env 为准。
def load_env_file(path: Path) -> None:
    import os

    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        if line.startswith("export "):
            line = line[len("export "):].strip()
        key, value = line.split("=", 1)
        key = key.strip()
        if not key:
            continue
        os.environ[key] = strip_env_quotes(value.strip())


# 去掉 .env 值两侧常见引号，保留 URL/API Key 等原始内容。
def strip_env_quotes(value: str) -> str:
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
        return value[1:-1]
    return value


# 规范化 OpenAI-compatible base url，复用仓库 Go demo 中自动补 /v1 的约定。
def normalize_openai_base_url(base_url: Optional[str]) -> Optional[str]:
    if not base_url:
        return None
    normalized = base_url.strip().rstrip("/")
    if not normalized or normalized.endswith("/v1"):
        return normalized
    return normalized + "/v1"


# 判断当前 embedding 配置是否是 Gemini embedContent 风格。
def is_gemini_embedding(model: str, base_url: Optional[str], endpoint_path: Optional[str]) -> bool:
    lowered_model = model.lower()
    lowered_base_url = (base_url or "").lower()
    lowered_endpoint_path = (endpoint_path or "").lower()
    return (
        lowered_model.startswith("google:")
        or "generativelanguage.googleapis.com" in lowered_base_url
        or ":embedcontent" in lowered_endpoint_path
    )


# 加载 .env 并组装 pipeline 配置；embedding 模型名来自 .env，不从 manifest 回退。
def load_pipeline_config(
    rag_dir: Path,
    env_file: Optional[Path],
    manifest_embedding_dim: int,
) -> PipelineConfig:
    import os

    repo_root = default_repo_root()
    resolved_env = env_file or repo_root / ".env"
    if resolved_env.exists():
        load_env_file(resolved_env)

    env = os.environ
    output_dir = Path(first_env(env, "RAG_OUTPUT_DIR") or repo_root / "output" / "rag")
    embedding_model = first_env(env, "RAG_EMBEDDING_MODEL", "EMBEDDING_MODEL", "LLM_MODEL")
    if not embedding_model:
        raise ConfigError("missing embedding model: set RAG_EMBEDDING_MODEL, EMBEDDING_MODEL, or LLM_MODEL in .env")
    endpoint_path = first_env(env, "RAG_EMBEDDING_ENDPOINT_PATH", "EMBEDDING_ENDPOINT_PATH")
    raw_base_url = first_env(
        env,
        "RAG_EMBEDDING_BASE_URL",
        "EMBEDDING_BASE_URL",
        "LLM_OPENAI_BASE_URL",
    )
    gemini_embedding = is_gemini_embedding(embedding_model, raw_base_url, endpoint_path)
    base_url = raw_base_url if gemini_embedding else normalize_openai_base_url(raw_base_url)
    api_key = first_env(
        env,
        "RAG_EMBEDDING_API_KEY",
        "EMBEDDING_API_KEY",
        "GOOGLE_API_KEY" if gemini_embedding else "OPENAI_API_KEY",
        "OPENAI_API_KEY",
    )
    embedder = EmbedderConfig(
        api_key=api_key,
        base_url=base_url,
        endpoint_path=endpoint_path,
        model=embedding_model,
        dim=env_int(env, "RAG_EMBEDDING_DIM", env_int(env, "EMBEDDING_DIM", manifest_embedding_dim)),
        batch_size=env_int(env, "RAG_EMBED_BATCH_SIZE", 32),
        timeout_seconds=env_float(env, "RAG_EMBED_TIMEOUT_SECONDS", 60.0),
        max_retries=env_int(env, "RAG_EMBED_MAX_RETRIES", 5),
    )
    return PipelineConfig(
        repo_root=repo_root,
        rag_dir=rag_dir,
        output_dir=output_dir,
        embedder=embedder,
        bm25_top_k=env_int(env, "RAG_BM25_TOP_K", 12),
        bm25_language=first_env(env, "RAG_BM25_LANGUAGE") or "zh",
    )
