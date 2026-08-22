"""Microsoft Entra authentication for Azure AI Foundry benchmark calls."""

from __future__ import annotations

import asyncio
import os
import threading
from collections.abc import AsyncGenerator, Callable

import httpx
import requests

FOUNDRY_SCOPE = "https://cognitiveservices.azure.com/.default"
GITHUB_OIDC_AUDIENCE = "api://AzureADTokenExchange"


def _required_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        raise RuntimeError(f"{name} is required for GitHub OIDC authentication")
    return value


def _github_oidc_assertion() -> str:
    """Request a fresh GitHub OIDC assertion for Azure workload federation."""
    response = requests.get(
        _required_env("ACTIONS_ID_TOKEN_REQUEST_URL"),
        headers={
            "Authorization": f"bearer {_required_env('ACTIONS_ID_TOKEN_REQUEST_TOKEN')}"
        },
        params={"audience": GITHUB_OIDC_AUDIENCE},
        timeout=10,
    )
    response.raise_for_status()
    assertion = response.json().get("value")
    if not isinstance(assertion, str) or not assertion:
        raise RuntimeError("GitHub OIDC response did not contain a non-empty value")
    return assertion


def _build_token_provider() -> Callable[[], str]:
    """Build a caching Entra token provider for CI or local keyless auth."""
    from azure.identity import (
        ClientAssertionCredential,
        DefaultAzureCredential,
        get_bearer_token_provider,
    )

    oidc_url = os.getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
    oidc_request_token = os.getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
    if bool(oidc_url) != bool(oidc_request_token):
        raise RuntimeError(
            "ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN "
            "must either both be set or both be absent"
        )

    if oidc_url:
        credential = ClientAssertionCredential(
            tenant_id=_required_env("AZURE_TENANT_ID"),
            client_id=_required_env("AZURE_CLIENT_ID"),
            func=_github_oidc_assertion,
        )
    else:
        credential = DefaultAzureCredential()

    return get_bearer_token_provider(credential, FOUNDRY_SCOPE)


class EntraBearerAuth(httpx.Auth):
    """Replace any SDK placeholder auth with a fresh Entra bearer token."""

    def __init__(self, token_provider: Callable[[], str] | None = None) -> None:
        self._token_provider = token_provider or _build_token_provider()
        self._provider_lock = threading.Lock()

    def _get_token(self) -> str:
        # Azure Identity caches and proactively refreshes the access token. The
        # lock prevents a concurrent first request/refresh from causing a burst
        # of identical assertion exchanges.
        with self._provider_lock:
            return self._token_provider()

    async def async_auth_flow(
        self, request: httpx.Request
    ) -> AsyncGenerator[httpx.Request, httpx.Response]:
        token = await asyncio.to_thread(self._get_token)
        request.headers.pop("api-key", None)
        request.headers["Authorization"] = f"Bearer {token}"
        yield request


def build_entra_auth() -> httpx.Auth:
    """Return HTTPX auth backed by an automatically refreshing Entra token."""
    return EntraBearerAuth()
