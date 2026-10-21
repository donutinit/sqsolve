# SPDX-License-Identifier: AGPL-3.0-only
"""Tests of check_error_codes.py: python3 -m unittest discover -s test -p 'test_*.py'."""
import contextlib
import io
import pathlib
import tempfile
import unittest

import check_error_codes as cec


def tree(test, files):
    """Writes files under a temporary directory removed when test ends."""
    tmp = tempfile.TemporaryDirectory()
    test.addCleanup(tmp.cleanup)
    root = pathlib.Path(tmp.name)
    for name, body in files.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body, encoding='utf-8')
    return str(root)


class CheckErrorCodes(unittest.TestCase):
    def test_two_meanings_for_one_code_fail(self):
        root = tree(self, {
            'core/backup.go': 'return errors.New("SQS-B033 unpin the backup before deleting it")\n',
            'core/library.go': 'return errors.New("SQS-B033 activity-based mode checks every 5 minutes")\n',
        })
        errors, _ = cec.check([root], set())
        self.assertEqual(len(errors), 3, errors)
        self.assertIn('SQS-B033 has 2 different messages:', errors[0])
        self.assertTrue(any('unpin the backup' in e for e in errors))
        self.assertTrue(any('activity-based mode' in e for e in errors))

    def test_same_message_in_two_places_passes(self):
        root = tree(self, {
            'a.go': 'errors.New("SQS-L002 library not found")\n',
            'b.go': 'fmt.Errorf("SQS-L002 library not found")\n',
        })
        self.assertEqual(cec.check([root], set()), ([], []))

    def test_format_verbs_case_and_final_period_are_ignored(self):
        root = tree(self, {
            'a.go': 'fmt.Errorf("SQS-B001 backup %s not found", id)\n',
            'b.go': 'fmt.Errorf("SQS-B001 Backup %d not found.", n)\n',
        })
        self.assertEqual(cec.check([root], set()), ([], []))

    def test_references_comments_and_tests_are_ignored(self):
        root = tree(self, {
            'a.go': 'errors.New("SQS-C054 the data folder lost its mode")\n',
            'b.go': 'http.Error(w, "Internal error (SQS-C054)", 500)\n'
                    '// SQS-C054 is returned when the dataset uses NFSv4 ACLs\n'
                    'code = "SQS-C054"\n',
            'a_test.go': 'if !strings.Contains(err.Error(), "SQS-C054 something else") {}\n',
        })
        self.assertEqual(cec.check([root], set()), ([], []))

    def test_allowlist_tolerates_a_known_code_and_notes_stale_entries(self):
        root = tree(self, {
            'a.go': 'errors.New("SQS-A030 your role does not allow this")\n',
            'b.go': 'errors.New("SQS-A030 the token scopes do not allow this")\n',
            'c.go': 'errors.New("SQS-A001 only one message")\n',
        })
        errors, notes = cec.check([root], {'SQS-A030', 'SQS-A001'})
        self.assertEqual(errors, [])
        self.assertEqual(len(notes), 1)
        self.assertIn('SQS-A001', notes[0])

    def test_main_exit_codes(self):
        bad = tree(self, {'a.go': 'errors.New("SQS-B033 one")\nerrors.New("SQS-B033 two")\n'})
        good = tree(self, {'a.go': 'errors.New("SQS-B033 one")\nerrors.New("SQS-B034 two")\n'})
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(cec.main(['--allowlist', '/dev/null', bad]), 1)
            self.assertEqual(cec.main(['--allowlist', '/dev/null', good]), 0)


if __name__ == '__main__':
    unittest.main()
