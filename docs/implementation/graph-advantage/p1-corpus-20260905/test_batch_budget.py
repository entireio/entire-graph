import json
import pathlib
import sys
import tempfile
import threading
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import batch_budget


class BatchBudgetContracts(unittest.TestCase):
    def identity(self):
        return {key: ((key[0] if key != 'build_manifest_sha256' else 'b') * 64)
                for key in batch_budget.REQUIRED_IDENTITY}

    def cell(self, worker=1, prep=0, arms=None, **overrides):
        value = {
            'worker': worker, 'repository': 'repo', 'profile': 'fast', 'verb': 'snapshot',
            'scenario': 'cold', 'trial': 0, 'arms': [False, True] if arms is None else arms,
            'preparatory_invocations': prep,
        }
        value.update(overrides)
        value['cell_id'] = batch_budget.cell_id(value)
        return value

    def document(self, cells):
        workers = {}
        for item in cells:
            workers[str(item['worker'])] = workers.get(str(item['worker']), 0) + item['preparatory_invocations'] + len(item['arms'])
        return {'version': 1, 'batch_id': 'batch-test', 'scope': 'bounded-diagnostic',
                'cap': 100, 'identity': self.identity(), 'cells': cells, 'workers': workers}

    def test_derives_cost_and_rejects_overflow_before_dispatch(self):
        cells = [self.cell(prep=1, trial=i) for i in range(34)]
        with self.assertRaises(batch_budget.BudgetError):
            batch_budget.validate(self.document(cells))

    def test_duplicate_cell_and_bad_allocation_are_rejected(self):
        cell = self.cell()
        with self.assertRaises(batch_budget.BudgetError):
            batch_budget.validate(self.document([cell, dict(cell)]))
        document = self.document([cell])
        document['workers'] = {'1': 1}
        with self.assertRaises(batch_budget.BudgetError):
            batch_budget.validate(document)

    def test_path_escape_and_semantic_cross_worker_duplicate_are_rejected(self):
        cell = self.cell(worker=1)
        duplicate = dict(cell, worker=2, arms=[True, False])
        duplicate['cell_id'] = batch_budget.cell_id(duplicate)
        document = self.document([cell, duplicate])
        document['workers'] = {'1': 2, '2': 2}
        document['batch_id'] = '../escape'
        with self.assertRaises(batch_budget.BudgetError):
            batch_budget.validate(document)
        document['batch_id'] = 'safe-batch'
        with self.assertRaises(batch_budget.BudgetError):
            batch_budget.validate(document)

    def test_worker_ledger_reserves_pair_and_counts_failed_start_without_retry(self):
        cell = self.cell(prep=1)
        batch = batch_budget.validate(self.document([cell]))
        with tempfile.TemporaryDirectory() as directory:
            ledger = batch_budget.WorkerLedger(pathlib.Path(directory) / 'ledger.json', batch, 1)
            ledger.start(cell['cell_id'], 'prep:0')
            ledger.finish(cell['cell_id'], 'prep:0', 'error')
            with self.assertRaises(batch_budget.BudgetError):
                ledger.start(cell['cell_id'], 'prep:0')
            ledger.start(cell['cell_id'], 'arm:false')
            with self.assertRaises(batch_budget.BudgetError):
                ledger.start(cell['cell_id'], 'arm:false')
            state = json.loads((pathlib.Path(directory) / 'ledger.json').read_text())
            self.assertEqual(len(state['started']), 2)
            self.assertEqual(state['failed'], [f"{cell['cell_id']}:prep:0"])

    def test_existing_ledger_refuses_restart(self):
        cell = self.cell(arms=[False])
        batch = batch_budget.validate(self.document([cell]))
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'ledger.json'
            batch_budget.WorkerLedger(path, batch, 1)
            with self.assertRaises(batch_budget.BudgetError):
                batch_budget.WorkerLedger(path, batch, 1)

    def test_concurrent_worker_claim_has_one_winner(self):
        cell = self.cell(arms=[False])
        batch = batch_budget.validate(self.document([cell]))
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'ledger.json'
            outcomes = []

            def claim():
                try:
                    batch_budget.WorkerLedger(path, batch, 1)
                    outcomes.append('won')
                except batch_budget.BudgetError:
                    outcomes.append('lost')

            threads = [threading.Thread(target=claim) for _ in range(2)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()
            self.assertEqual(sorted(outcomes), ['lost', 'won'])


if __name__ == '__main__':
    unittest.main()
