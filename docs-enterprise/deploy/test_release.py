import copy
import unittest
from unittest.mock import patch
import release

IMAGE = (
    "061717858829.dkr.ecr.us-east-1.amazonaws.com/odigos-enterprise-docs@sha256:"
    + "a" * 64
)
OLD = IMAGE.replace("a" * 64, "b" * 64)
STACK = {
    "StackStatus": "UPDATE_COMPLETE",
    "Parameters": [
        {"ParameterKey": "ImageUri", "ParameterValue": OLD},
        {"ParameterKey": "ParentHostedZoneId", "ParameterValue": "parent"},
        {"ParameterKey": "SessionKeyVersion", "ParameterValue": "7"},
        {"ParameterKey": "MaxConcurrency", "ParameterValue": "100"},
    ],
    "Outputs": [
        {"OutputKey": "FunctionName", "OutputValue": "docs-function"},
        {"OutputKey": "DocsURL", "OutputValue": "https://enterprise.docs.odigos.io"},
    ],
}


class ReleaseTests(unittest.TestCase):
    def test_updates_only_image_and_preserves_template_and_parameters(self):
        import json

        with patch.object(release, "aws") as aws:
            release.update_image("docs", STACK, IMAGE)
        request = json.loads(aws.call_args_list[0].args[-1])
        self.assertTrue(request["UsePreviousTemplate"])
        self.assertNotIn("TemplateBody", request)
        self.assertEqual(request["Parameters"][0]["ParameterValue"], IMAGE)
        self.assertTrue(all(p["UsePreviousValue"] for p in request["Parameters"][1:]))
        self.assertEqual(
            aws.call_args_list[1].args[:3],
            ("cloudformation", "wait", "stack-update-complete"),
        )

    def run_release(self, deployed_image=IMAGE, boundary_error=None, stack=None):
        with (
            patch.object(release, "stack_info", return_value=stack or STACK),
            patch.object(release, "update_image") as update,
            patch.object(
                release,
                "aws",
                return_value={
                    "Code": {"ResolvedImageUri": deployed_image},
                    "Configuration": {"State": "Active"},
                },
            ),
            patch.object(release, "verify_public_boundary", side_effect=boundary_error),
            patch.object(release.time, "sleep"),
        ):
            try:
                release.release("docs", IMAGE)
                return update.call_args_list, None
            except RuntimeError as error:
                return update.call_args_list, error

    def test_success(self):
        calls, error = self.run_release()
        self.assertIsNone(error)
        self.assertEqual([c.args[2] for c in calls], [IMAGE])

    def test_idempotent_release_still_verifies(self):
        stack = copy.deepcopy(STACK)
        stack["Parameters"][0]["ParameterValue"] = IMAGE
        calls, error = self.run_release(stack=stack)
        self.assertIsNone(error)
        self.assertEqual(calls, [])

    def test_wrong_image_is_rolled_back(self):
        calls, error = self.run_release(deployed_image=OLD)
        self.assertIsNotNone(error)
        self.assertEqual([c.args[2] for c in calls], [IMAGE, OLD])

    def test_failed_access_boundary_is_rolled_back(self):
        calls, error = self.run_release(
            boundary_error=RuntimeError("private assets became public")
        )
        self.assertIsNotNone(error)
        self.assertEqual([c.args[2] for c in calls], [IMAGE, OLD])

    def test_busy_stack_is_not_mutated(self):
        calls, error = self.run_release(
            stack={**STACK, "StackStatus": "UPDATE_IN_PROGRESS"}
        )
        self.assertIsNotNone(error)
        self.assertEqual(calls, [])

    def test_mutable_tags_are_rejected_before_aws(self):
        with patch.object(release, "aws") as aws, self.assertRaises(ValueError):
            release.release("docs", IMAGE.split("@")[0] + ":latest")
        aws.assert_not_called()


if __name__ == "__main__":
    unittest.main()
