#!/usr/bin/env python3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import license_check


class ValidateRootNoticeTest(unittest.TestCase):
    def test_accepts_reviewed_notice_text_present_in_root_notice(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            module_dir = Path(tmpdir) / "module"
            module_dir.mkdir()
            (module_dir / "NOTICE").write_text("Reviewed notice line 1\nReviewed notice line 2\n")
            root_notice = Path(tmpdir) / "NOTICE"
            root_notice.write_text("Header\n\nReviewed notice line 1\nReviewed notice line 2\n")

            failures = license_check.validate_root_notice(
                [
                    {
                        "module": "github.com/prometheus/common",
                        "version": "v0.66.1",
                        "module_dir": str(module_dir),
                        "notice_files": "NOTICE",
                    }
                ],
                root_notice,
            )

        self.assertEqual(failures, [])

    def test_rejects_missing_reviewed_notice_text(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            module_dir = Path(tmpdir) / "module"
            module_dir.mkdir()
            (module_dir / "NOTICE").write_text("Expected notice body\n")
            root_notice = Path(tmpdir) / "NOTICE"
            root_notice.write_text("Header only\n")

            failures = license_check.validate_root_notice(
                [
                    {
                        "module": "github.com/prometheus/common",
                        "version": "v0.66.1",
                        "module_dir": str(module_dir),
                        "notice_files": "NOTICE",
                    }
                ],
                root_notice,
            )

        self.assertEqual(
            failures,
            [
                "github.com/prometheus/common v0.66.1: root NOTICE does not include reviewed NOTICE text from NOTICE"
            ],
        )


if __name__ == "__main__":
    unittest.main()
