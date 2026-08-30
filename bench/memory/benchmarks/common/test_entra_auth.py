"""Tests for refreshable Microsoft Entra authentication."""

from __future__ import annotations

import asyncio
import os
import threading
import time
import unittest
from unittest.mock import Mock, patch

import httpx

try:
    from benchmarks.common import entra_auth
except ModuleNotFoundError:
    from bench.memory.benchmarks.common import entra_auth


class EntraBearerAuthTests(unittest.TestCase):
    def test_auth_replaces_api_key_and_placeholder_bearer_each_request(self) -> None:
        tokens = iter(("first-token", "refreshed-token"))
        seen_headers: list[httpx.Headers] = []

        async def exercise() -> None:
            def handle(request: httpx.Request) -> httpx.Response:
                seen_headers.append(request.headers)
                return httpx.Response(200, json={"ok": True})

            async with httpx.AsyncClient(
                auth=entra_auth.EntraBearerAuth(lambda: next(tokens)),
                transport=httpx.MockTransport(handle),
            ) as client:
                for _ in range(2):
                    response = await client.get(
                        "https://example.test/models/chat/completions",
                        headers={
                            "Authorization": "Bearer sdk-placeholder",
                            "api-key": "must-not-leak",
                        },
                    )
                    self.assertEqual(response.status_code, 200)

        asyncio.run(exercise())
        self.assertEqual(
            [headers["Authorization"] for headers in seen_headers],
            ["Bearer first-token", "Bearer refreshed-token"],
        )
        self.assertTrue(all("api-key" not in headers for headers in seen_headers))

    def test_concurrent_requests_serialize_token_provider_calls(self) -> None:
        call_count = 0
        active_calls = 0
        max_active_calls = 0
        counter_lock = threading.Lock()

        def token_provider() -> str:
            nonlocal call_count, active_calls, max_active_calls
            with counter_lock:
                call_count += 1
                active_calls += 1
                max_active_calls = max(max_active_calls, active_calls)
            time.sleep(0.02)
            with counter_lock:
                active_calls -= 1
            return "access-token"

        async def exercise() -> None:
            transport = httpx.MockTransport(lambda _: httpx.Response(200))
            async with httpx.AsyncClient(
                auth=entra_auth.EntraBearerAuth(token_provider),
                transport=transport,
            ) as client:
                responses = await asyncio.gather(
                    *[client.get(f"https://example.test/request/{i}") for i in range(8)]
                )
                self.assertTrue(all(response.status_code == 200 for response in responses))

        asyncio.run(exercise())
        # Auth asks Azure Identity's caching provider for a token on every
        # request. Only the provider itself decides when the token needs refresh.
        self.assertEqual(call_count, 8)
        self.assertEqual(max_active_calls, 1)

    @patch.dict(
        os.environ,
        {
            "ACTIONS_ID_TOKEN_REQUEST_URL": "https://github.test/oidc?api-version=1",
            "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runner-request-token",
        },
        clear=True,
    )
    @patch.object(entra_auth.requests, "get")
    def test_github_assertion_requests_azure_audience(self, get: Mock) -> None:
        get.return_value.json.return_value = {"value": "fresh-assertion"}

        assertion = entra_auth._github_oidc_assertion()

        self.assertEqual(assertion, "fresh-assertion")
        get.assert_called_once_with(
            "https://github.test/oidc?api-version=1",
            headers={"Authorization": "bearer runner-request-token"},
            params={"audience": "api://AzureADTokenExchange"},
            timeout=10,
        )
        get.return_value.raise_for_status.assert_called_once_with()

    @patch.dict(
        os.environ,
        {
            "ACTIONS_ID_TOKEN_REQUEST_URL": "https://github.test/oidc",
            "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runner-request-token",
            "AZURE_TENANT_ID": "tenant-id",
            "AZURE_CLIENT_ID": "client-id",
        },
        clear=True,
    )
    @patch("azure.identity.get_bearer_token_provider")
    @patch("azure.identity.ClientAssertionCredential")
    def test_ci_uses_refreshable_client_assertion_credential(
        self, credential: Mock, bearer_provider: Mock
    ) -> None:
        provider = Mock(return_value="access-token")
        bearer_provider.return_value = provider

        result = entra_auth._build_token_provider()

        self.assertIs(result, provider)
        credential.assert_called_once_with(
            tenant_id="tenant-id",
            client_id="client-id",
            func=entra_auth._github_oidc_assertion,
        )
        bearer_provider.assert_called_once_with(
            credential.return_value, entra_auth.FOUNDRY_SCOPE
        )

    @patch.dict(os.environ, {}, clear=True)
    @patch("azure.identity.get_bearer_token_provider")
    @patch("azure.identity.DefaultAzureCredential")
    def test_local_run_uses_default_azure_credential(
        self, credential: Mock, bearer_provider: Mock
    ) -> None:
        provider = Mock(return_value="access-token")
        bearer_provider.return_value = provider

        result = entra_auth._build_token_provider()

        self.assertIs(result, provider)
        credential.assert_called_once_with()
        bearer_provider.assert_called_once_with(
            credential.return_value, entra_auth.FOUNDRY_SCOPE
        )

    @patch.dict(
        os.environ,
        {"ACTIONS_ID_TOKEN_REQUEST_URL": "https://github.test/oidc"},
        clear=True,
    )
    def test_partial_github_oidc_environment_fails_closed(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "must either both be set"):
            entra_auth._build_token_provider()


if __name__ == "__main__":
    unittest.main()
