import json
from pathlib import Path
import tempfile
import threading
import unittest

from wallet_sessions import sync_one


class SessionResultTests(unittest.TestCase):
    def invoke(self, body, expected_tip=3471422, timeout=2):
        with tempfile.TemporaryDirectory() as root:
            directory = Path(root)
            binary = directory / 'fixture'
            binary.write_text('#!/usr/bin/env python3\n' + body)
            binary.chmod(0o700)
            result, _, _ = sync_one(str(binary), directory, 'http://127.0.0.1:19068',
                                    1, timeout, threading.Barrier(1), expected_tip)
            saved = json.loads((directory / 'result.json').read_text())
            self.assertEqual(result, saved)
            return result

    def test_complete_wallet_requires_matching_tip(self):
        body = ('print(\'{"is_complete":true,"chain_tip_height":3471422,"scanned_height":3471422}\')\n'
                'print(\'{"operation":"sync","success":true}\')\n')
        self.assertTrue(self.invoke(body)['success'])
        self.assertFalse(self.invoke(body, expected_tip=3471423)['success'])

    def test_partial_or_failed_wallet_is_not_success(self):
        self.assertFalse(self.invoke('print(\'{"operation":"sync","success":true}\')\n')['success'])
        self.assertFalse(self.invoke('raise SystemExit(2)\n')['success'])

    def test_timeout_reaps_the_wallet(self):
        result = self.invoke('import time\ntime.sleep(10)\n', timeout=0.05)
        self.assertTrue(result['timed_out'])
        self.assertFalse(result['success'])
        self.assertLess(result['elapsed_seconds'], 2)


if __name__ == '__main__':
    unittest.main()
