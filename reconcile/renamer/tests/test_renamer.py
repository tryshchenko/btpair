import csv
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

import llm  # noqa: E402
import names  # noqa: E402
import renamer  # noqa: E402


class Rules(unittest.TestCase):
    def test_release_names(self):
        for src, want in {
            "The.Night.Garden.S07E11.720p.BluRay.x264-GalaxyTV": "The Night Garden S07E11",
            "The.Silver.Lake.1976.WEB-DL.1080p": "The Silver Lake (1976)",
            "Harbor [1989 WEB-DL 1080p]": "Harbor (1989)",
            "[EN] Northwind (1997)1080p h264 Ac3 5.1 Ita Eng Sub Ita Eng-MIRCrew": "[EN] Northwind (1997)",
            "Зимові.вечори.1965.BDRip.720p-ylnian": "Зимові вечори (1965)",
            "Quiet Days.Тихие дни.2024.WEB-DL.1080p": "Quiet Days Тихие дни (2024)",
            "720p-anna-27-paints-the-harbor (1)": "anna 27 paints the harbor (1)",
            "Morning Walk ExampleVideos.com": "Morning Walk",
            "МояМузыка.рф - Jane Doe - Winter Workshop": "Jane Doe - Winter Workshop",
            "Clip name.mp4": "Clip name",
            "01-river_band-the_long_road": "01 - river band-the long road",
            "Blue.Room.2001.CC.x264.DTS-WAF": "Blue Room (2001)",   # ".CC" is Criterion, not a website
            "Show.Me.Tv.2010.720p": "Show Me Tv (2010)",
        }.items():
            self.assertEqual(names.rule_clean(src), want, src)

    def test_leaves_ordinary_names_alone(self):
        # words that are also release tags, hyphenated words, IDs, emphasis
        for s in ["Anna, Pieter en de Kat", "The Extended Family", "Proper English", "cover-back",
                  "1234567_video_2020-05-30_17-06", "07_ukr", "02 - Rain On Glass", "menu-ost2-button",
                  "Learn to COOK with CONFIDENCE! TIPS and TRICKS", "Mr. Robot", "Vol. 2 - Live"]:
            self.assertEqual(names.rule_clean(s), s, s)
            self.assertFalse(names.needs_model(names.rule_clean(s)), s)

    def test_model_routing(self):
        self.assertTrue(names.needs_model("THE GREEN FIELDS AND THE QUIET RIVERS"))
        self.assertTrue(names.needs_model("anna 27 paints the harbor"))
        self.assertFalse(names.needs_model("Anna 27 Paints the Harbor"))

    def test_skip(self):
        self.assertEqual(names.skip_reason("IMG_1234.JPG"), "camera name")
        self.assertEqual(names.skip_reason("2015_0628_074138_001.MOV"), "camera name")
        self.assertEqual(names.skip_reason("PXL_20230101_120000123.jpg"), "camera name")
        self.assertEqual(names.skip_reason("main.go"), "not a media file")
        self.assertEqual(names.skip_reason(".DS_Store"), "hidden file")
        self.assertIsNone(names.skip_reason("The.Movie.2017.1080p.mkv"))

    def test_validate(self):
        ok = lambda o, p: names.validate(o, p)[0]
        self.assertTrue(ok("THE GREEN FIELDS", "The Green Fields"))
        self.assertFalse(ok("Поиск аудиозаписей", "Poisk Audiozapisey"))        # transliterated
        self.assertFalse(ok("Some cooking lesson", "Зимові вечори (1965)"))  # copied an example
        self.assertFalse(ok("Northwind (1997)", "Northwind (1.99)"))                  # year lost
        self.assertFalse(ok("name", "a/b"))
        self.assertTrue(ok('Say "hi" | there', 'Say "Hi" | There'))              # chars already in the original
        self.assertFalse(ok("plain", 'plain"'))

    def test_title_case(self):
        self.assertEqual(names.title_case("anna had painted the old harbor and the boats"), "Anna Had Painted the Old Harbor and the Boats")
        self.assertEqual(names.title_case("24 - end of the road"), "24 - End of the Road")
        self.assertEqual(names.title_case("Already Mixed case name"), "Already Mixed case name")
        self.assertEqual(names.title_case("тихий дон и море"), "тихий дон и море")  # non-ASCII: the model's job

    def test_polish(self):
        self.assertEqual(names.polish("24 - End Of The Road"), "24 - End of the Road")
        self.assertEqual(names.polish("The Pretty Duo Of Aurora And Lenna"), "The Pretty Duo of Aurora and Lenna")
        self.assertEqual(names.polish("Of Mice And Men"), "Of Mice and Men")
        self.assertEqual(names.polish("Anna Has A Garden"), "Anna Has a Garden")
        self.assertEqual(names.polish("Тихий Дон"), "Тихий Дон")


class Memory(unittest.TestCase):
    def test_refuses_without_enough_memory(self):
        with mock.patch.object(llm, "memory", return_value=(16 * llm.GiB, 2 * llm.GiB)):
            with self.assertRaises(llm.NotEnoughMemory) as e:
                llm.check_memory(int(1.4e9))
            self.assertIn("free", str(e.exception))
        with mock.patch.object(llm, "memory", return_value=(4 * llm.GiB, 3.5 * llm.GiB)):
            with self.assertRaises(llm.NotEnoughMemory) as e:
                llm.check_memory(int(2.5e9))
            self.assertIn("RAM", str(e.exception))
        with mock.patch.object(llm, "memory", return_value=(16 * llm.GiB, 1 * llm.GiB)):
            llm.check_memory(int(1.4e9), already_loaded=int(2e9))  # same model already resident: fine
        with mock.patch.object(llm, "memory", return_value=(16 * llm.GiB, 8 * llm.GiB)):
            need, _ = llm.check_memory(int(1.4e9))
            self.assertLess(need, 3 * llm.GiB)

    def test_reads_real_memory(self):
        total, avail = llm.memory()
        self.assertGreater(total, avail)
        self.assertGreater(avail, 0)


class EndToEnd(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.root = os.path.join(self.tmp, "Media")
        self.out = os.path.join(self.tmp, "plans")
        files = [
            "Movies/The.Silver.Lake.1976.WEB-DL.1080p.mkv",
            "Movies/The.Silver.Lake.1976.WEB-DL.1080p.srt",
            "Movies/The.Silver.Lake.1976.WEB-DL.1080p.en.srt",
            "Movies/Harbor [1989 WEB-DL 1080p].mkv",
            "Movies/Harbor (1989).mkv",                      # collision target already exists
            "Photos/IMG_1234.JPG",
            "Photos/2015_0628_074138_001.MOV",
            "Code/app/package.json",
            "Code/app/The.Silver.Lake.1976.WEB-DL.1080p.mkv",  # inside a code project
            "Lib.photoslibrary/originals/The.Movie.2017.1080p.mkv",
            "Music/02 - Rain On Glass.mp3",
            "Music/THE GREEN FIELDS AND THE QUIET RIVERS.mp3",
        ]
        for f in files:
            p = os.path.join(self.root, f)
            os.makedirs(os.path.dirname(p), exist_ok=True)
            with open(p, "w") as fh:
                fh.write(f)
        renamer.OUT_DIR = self.out

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def run_plan(self, *extra):
        args = renamer.argparse.Namespace(folder=self.root, model="x", no_model=True, batch=10, *extra)
        with mock.patch("sys.stdout", new=io.StringIO()):
            renamer.plan(args)
        plan = [os.path.join(self.out, f) for f in os.listdir(self.out) if f.startswith("plan-")][0]
        with open(plan, encoding="utf-8") as f:
            return plan, list(csv.DictReader(f, delimiter="\t"))

    def test_plan_apply_undo(self):
        before = sorted(os.path.relpath(os.path.join(d, f), self.root) for d, _, fs in os.walk(self.root) for f in fs)
        plan, rows = self.run_plan()
        got = {os.path.relpath(r["path"], self.root): r["new_name"] for r in rows}
        self.assertEqual(got["Movies/The.Silver.Lake.1976.WEB-DL.1080p.mkv"], "The Silver Lake (1976).mkv")
        self.assertEqual(got["Movies/The.Silver.Lake.1976.WEB-DL.1080p.srt"], "The Silver Lake (1976).srt")
        self.assertEqual(got["Movies/The.Silver.Lake.1976.WEB-DL.1080p.en.srt"], "The Silver Lake (1976).en.srt")
        self.assertEqual(got["Movies/Harbor [1989 WEB-DL 1080p].mkv"], "Harbor (1989) (2).mkv")  # no clobbering
        for untouched in ["Photos/IMG_1234.JPG", "Photos/2015_0628_074138_001.MOV", "Music/02 - Rain On Glass.mp3",
                          "Code/app/The.Silver.Lake.1976.WEB-DL.1080p.mkv", "Lib.photoslibrary/originals/The.Movie.2017.1080p.mkv"]:
            self.assertNotIn(untouched, got)
        self.assertEqual(os.listdir(self.root + "/Movies").count("Harbor (1989).mkv"), 1)  # plan renamed nothing

        # skip one row in review, then apply
        with open(plan, encoding="utf-8") as f:
            text = f.read().replace("yes\t" + os.path.join(self.root, "Movies/Harbor"), "no\t" + os.path.join(self.root, "Movies/Harbor"))
        with open(plan, "w", encoding="utf-8") as f:
            f.write(text)
        with mock.patch("sys.stdout", new=io.StringIO()):
            renamer.apply(renamer.argparse.Namespace(plan=plan))
        self.assertTrue(os.path.exists(os.path.join(self.root, "Movies/The Silver Lake (1976).mkv")))
        self.assertTrue(os.path.exists(os.path.join(self.root, "Movies/The Silver Lake (1976).en.srt")))
        self.assertTrue(os.path.exists(os.path.join(self.root, "Movies/Harbor [1989 WEB-DL 1080p].mkv")))  # was set to no

        log = [os.path.join(self.out, f) for f in os.listdir(self.out) if f.startswith("applied-")][0]
        with mock.patch("sys.stdout", new=io.StringIO()):
            renamer.undo(renamer.argparse.Namespace(log=log))
        after = sorted(os.path.relpath(os.path.join(d, f), self.root) for d, _, fs in os.walk(self.root) for f in fs)
        self.assertEqual(after, before)

    def test_files_vanishing_mid_run(self):
        # a folder deleted between scanning and writing the plan is skipped
        orig = renamer.collect

        def collect_then_delete(root):
            cands, stats = orig(root)
            shutil.rmtree(os.path.join(self.root, "Music"))
            return cands, stats

        with mock.patch.object(renamer, "collect", collect_then_delete):
            plan, rows = self.run_plan()
        self.assertFalse(any("/Music/" in r["path"] for r in rows))
        self.assertTrue(any("Silver.Lake" in r["path"] for r in rows))

    def test_hidden_folders_are_skipped(self):
        p = os.path.join(self.root, ".trash", "The.Movie.2017.1080p.mkv")
        os.makedirs(os.path.dirname(p))
        with open(p, "w") as f:
            f.write("x")
        plan, rows = self.run_plan()
        self.assertFalse(any("/.trash/" in r["path"] for r in rows))

    def test_refuses_replica(self):
        with mock.patch.object(renamer, "btpair_role", return_value="replica"):
            with self.assertRaises(SystemExit) as e:
                renamer.plan(renamer.argparse.Namespace(folder=self.root, model="x", no_model=True, batch=10))
            self.assertIn("replica", str(e.exception))

    def test_memory_refusal_stops_before_model(self):
        args = renamer.argparse.Namespace(folder=self.root, model="qwen3:1.7b", no_model=False, batch=10)
        with mock.patch.object(llm, "Server"), mock.patch.object(llm, "model_size", return_value=int(1.4e9)), \
             mock.patch.object(llm, "loaded_size", return_value=0), \
             mock.patch.object(llm, "memory", return_value=(16 * llm.GiB, 1 * llm.GiB)), \
             mock.patch.object(llm, "clean_batch") as cb, mock.patch("sys.stdout", new=io.StringIO()):
            with self.assertRaises(SystemExit) as e:
                renamer.plan(args)
        self.assertIn("not starting the model", str(e.exception))
        cb.assert_not_called()
        self.assertFalse(os.path.exists(self.out) and os.listdir(self.out))


if __name__ == "__main__":
    unittest.main()
