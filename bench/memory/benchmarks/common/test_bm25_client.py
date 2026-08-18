import os
import tempfile
import unittest
from unittest.mock import patch

from bench.memory.benchmarks.common.bm25_client import Bm25Client


class Bm25ClientSearchTest(unittest.IsolatedAsyncioTestCase):
    async def test_negative_scores_with_lexical_overlap_are_returned(self) -> None:
        with tempfile.TemporaryDirectory() as state_root, patch.dict(
            os.environ,
            {
                "BM25_STATE_ROOT": state_root,
                "BM25_STEM": "0",
                "BM25_STOPWORDS": "0",
            },
        ):
            client = Bm25Client()
            user_id = "negative-idf"
            await client.add(
                [
                    {"role": "user", "content": "launch code"},
                    {"role": "user", "content": "launch code"},
                    {"role": "user", "content": "weather forecast"},
                ],
                user_id=user_id,
            )

            results = await client.search("launch code", user_id=user_id, top_k=2)

            self.assertEqual(2, len(results))
            self.assertTrue(all(result["score"] < 0 for result in results))
            self.assertTrue(
                all(result["memory"] == "user: launch code" for result in results)
            )


if __name__ == "__main__":
    unittest.main()
