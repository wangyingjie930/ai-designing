import json
import time
from typing import List, Optional
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qsl, urlencode, urlparse, urlunparse
from urllib.request import Request, urlopen

from llama_index.core.base.embeddings.base import BaseEmbedding, Embedding
from pydantic import Field


# GeminiEmbedding 让 LlamaIndex IngestionPipeline 能直接调用真实 Gemini embedContent。
class GeminiEmbedding(BaseEmbedding):
    api_key: str = Field(exclude=True)
    base_url: str
    endpoint_path: Optional[str] = None
    output_dimensionality: int
    timeout_seconds: float = 60.0
    max_retries: int = 5

    # 查询向量和文档向量都走同一个真实 embedContent 端点，保持检索空间一致。
    def _get_query_embedding(self, query: str) -> Embedding:
        return self._get_text_embedding(query)

    # 异步接口复用同步请求，满足 LlamaIndex BaseEmbedding 契约。
    async def _aget_query_embedding(self, query: str) -> Embedding:
        return self._get_query_embedding(query)

    # 单条文本真实请求 Gemini embedContent。
    def _get_text_embedding(self, text: str) -> Embedding:
        return self._request_embedding(text)

    # 批量接口逐条调用真实端点，避免伪造向量或绕过 provider 限制。
    def _get_text_embeddings(self, texts: List[str]) -> List[Embedding]:
        return [self._request_embedding(text) for text in texts]

    # 调用 Gemini embedContent，并对临时错误做有限重试。
    def _request_embedding(self, text: str) -> Embedding:
        last_error = None
        for attempt in range(max(self.max_retries, 1)):
            try:
                return self._post_embedding(text)
            except (HTTPError, URLError) as exc:
                last_error = exc
                if isinstance(exc, HTTPError) and exc.code < 500 and exc.code != 429:
                    raise self._format_http_error(exc)
                time.sleep(min(2 ** attempt, 8))
        if isinstance(last_error, HTTPError):
            raise self._format_http_error(last_error)
        raise RuntimeError("Gemini embedding request failed: %s" % last_error)

    # 发送 JSON 请求并解析 embedding.values。
    def _post_embedding(self, text: str) -> Embedding:
        body = {
            "model": "models/" + self._clean_model(),
            "content": {"parts": [{"text": text}]},
        }
        if self.output_dimensionality > 0:
            body["outputDimensionality"] = self.output_dimensionality
        payload = json.dumps(body, ensure_ascii=False).encode("utf-8")
        request = Request(
            self._endpoint_url(),
            data=payload,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urlopen(request, timeout=self.timeout_seconds) as response:
            parsed = json.loads(response.read().decode("utf-8"))
        values = parsed.get("embedding", {}).get("values", [])
        if not values:
            raise RuntimeError("Gemini embedding response has no vector")
        return [float(value) for value in values]

    # 拼出最终 Gemini endpoint，并追加 key 查询参数。
    def _endpoint_url(self) -> str:
        endpoint_path = (self.endpoint_path or ("models/" + self._clean_model() + ":embedContent")).strip("/")
        parsed = urlparse(self.base_url.rstrip("/") + "/" + endpoint_path)
        query = dict(parse_qsl(parsed.query, keep_blank_values=True))
        query.setdefault("key", self.api_key)
        return urlunparse(parsed._replace(query=urlencode(query)))

    # 去掉 provider 前缀，兼容 google:gemini-embedding-001 和 models/... 两种写法。
    def _clean_model(self) -> str:
        model = self.model_name.strip()
        if ":" in model and not model.startswith("models/"):
            model = model.split(":", 1)[1]
        return model.removeprefix("models/")

    # 把 HTTP 错误体压缩后返回，方便定位真实 provider 拒绝原因。
    def _format_http_error(self, exc: HTTPError) -> RuntimeError:
        body = exc.read().decode("utf-8", errors="replace")
        return RuntimeError("Gemini embedding request failed: status=%s body=%s" % (exc.code, _compact_body(body)))


# 压缩 provider 错误体，避免终端输出过长响应。
def _compact_body(body: str) -> str:
    compacted = " ".join(body.split())
    if len(compacted) <= 300:
        return compacted
    return compacted[:300] + "...(truncated %d bytes)" % len(body.encode("utf-8"))
