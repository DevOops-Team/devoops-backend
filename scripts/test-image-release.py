#!/usr/bin/env python3
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('image-release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
DIGEST = 'sha256:' + 'a' * 64
TREE = 'b' * 40


class ReleaseTests(unittest.TestCase):
    def env(self, root, branch='main', target='prod'):
        return dict(WORKSPACE=root, SOURCE_BRANCH=branch, DEPLOY_ENV=target, IMAGE_NAME=release.IMAGE,
                    SOURCE_TREE=TREE, SOURCE_COMMIT='c'*40, PROMOTE_DIGEST=DIGEST, BUILD_NUMBER='42')

    def test_explicit_digest_is_pinned_without_any_staging_latest_lookup(self):
        with tempfile.TemporaryDirectory() as root:
            env = self.env(root)
            env['PROMOTE_DIGEST'] = '  '+DIGEST+'\n'
            release.prepare_prod(env)
            self.assertEqual(Path(root, '.deploy-digest').read_text().strip(), DIGEST)
            seen = []
            def inspect(image):
                seen.append(image)
                return {release.TREE_LABEL:TREE}
            release.verify_prod(env, inspect)
            self.assertEqual(seen, [release.IMAGE+'@'+DIGEST])

    def test_missing_malformed_tag_or_foreign_image_digest_is_rejected(self):
        for value in ('', 'latest', 'tree-'+TREE[:12], 'sha256:bad', 'sha256:'+'A'*64,
                      'other/image@'+DIGEST, DIGEST+';echo bad'):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as root:
                env=self.env(root); env['PROMOTE_DIGEST']=value
                with self.assertRaises(ValueError): release.prepare_prod(env)
                self.assertFalse(Path(root, '.deploy-digest').exists())

    def test_source_mismatch_or_missing_image_labels_blocks_promotion(self):
        for labels in (None, {}, {release.TREE_LABEL:'d'*40}, {release.TREE_LABEL:TREE[:12]}):
            with self.subTest(labels=labels), tempfile.TemporaryDirectory() as root:
                env=self.env(root); release.prepare_prod(env)
                with self.assertRaises(ValueError): release.verify_prod(env, lambda _:labels)

    def test_branch_environment_repository_and_tree_guards(self):
        for key,value in [('SOURCE_BRANCH','develop'), ('DEPLOY_ENV','staging'), ('IMAGE_NAME','other/image'),
                          ('SOURCE_TREE',''), ('SOURCE_TREE',TREE[:12])]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as root:
                env=self.env(root);env[key]=value
                with self.assertRaises(ValueError): release.prepare_prod(env)
                self.assertFalse(Path(root, '.deploy-digest').exists())

    def test_staging_release_records_the_exact_pushed_digest_and_source(self):
        with tempfile.TemporaryDirectory() as root:
            env=self.env(root,'develop','staging')
            Path(root,'.deploy-digest').write_text(DIGEST)
            Path(root,'.staging-infra-revision').write_text('d'*40)
            release.record_staging(env)
            data=json.loads(Path(root,'release.json').read_text())
            self.assertEqual(data, dict(image=release.IMAGE, digest=DIGEST, source_commit='c'*40,
                                       source_tree=TREE, staging_manifest_revision='d'*40, build_number='42'))
            # A later staging release cannot change the digest selected for a main run.
            selected=data['digest']
            Path(root,'.deploy-digest').write_text('sha256:'+'e'*64)
            env=self.env(root);env['PROMOTE_DIGEST']=selected
            release.prepare_prod(env)
            self.assertEqual(Path(root,'.deploy-digest').read_text().strip(), DIGEST)

    def test_release_record_requires_successful_manifest_revision(self):
        with tempfile.TemporaryDirectory() as root:
            env=self.env(root,'develop','staging')
            Path(root,'.deploy-digest').write_text(DIGEST)
            with self.assertRaises(FileNotFoundError): release.record_staging(env)
            Path(root,'.staging-infra-revision').write_text('bad-revision')
            with self.assertRaises(ValueError): release.record_staging(env)
            self.assertFalse(Path(root,'release.json').exists())

    def test_corrupt_digest_never_reaches_registry_inspection(self):
        with tempfile.TemporaryDirectory() as root:
            Path(root,'.deploy-digest').write_text('not-a-digest')
            def inspect(_):
                self.fail('registry inspection must not run for an invalid digest')
            with self.assertRaises(ValueError): release.verify_prod(self.env(root), inspect)

    def test_pipeline_removes_promotion_tags_and_archives_digest_after_staging_update(self):
        text=Path(__file__).parent.parent.joinpath('Jenkinsfile').read_text()
        self.assertNotIn("stage('태그 발행",text)
        self.assertNotIn('docker tag',text)
        self.assertNotIn(':tree-',text)
        self.assertIn('docker pull -q "$IMAGE_NAME@$DIGEST"',text)
        self.assertIn('--label "io.devoops.source-tree=$SOURCE_TREE"',text)
        main=text.split("stage('이미지 조회 [main]')",1)[1].split("stage('",1)[0]
        self.assertLess(main.index('docker pull'),main.index('verify-prod'))
        self.assertIn('prepare-prod',text.split("stage('도구 확인",1)[0])
        manifest=text.split("stage('매니페스트 갱신 [develop·main]')",1)[1].split("stage('",1)[0]
        self.assertLess(manifest.index('update-image.sh'),manifest.index('record-staging'))
        self.assertLess(manifest.index('ACTUAL'),manifest.index('archiveArtifacts'))
        self.assertIn("env.DEPLOY_ENV == 'staging'",manifest)


if __name__ == '__main__': unittest.main()
