#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""

import argparse
import copy
import io
import json
import os
import shutil
import subprocess
import sys
import tarfile
from concurrent.futures import ProcessPoolExecutor
from dataclasses import dataclass
from datetime import datetime, timedelta
from distutils.dir_util import copy_tree
from queue import Queue
from typing import Any, Callable, Dict, Iterable, List, Optional, Set, Tuple, Union

import requests
import semver
from sonar.sonar import process_image

import docker
from scripts.evergreen.release.agent_matrix import (
    get_supported_version_for_image_matrix_handling,
)
from scripts.evergreen.release.base_logger import logger
from scripts.evergreen.release.images_signing import (
    mongodb_artifactory_login,
    sign_image,
    verify_signature,
)

DEFAULT_IMAGE_TYPE = "ubi"
DEFAULT_NAMESPACE = "default"

# QUAY_REGISTRY_URL sets the base registry for all release build stages. Context images and daily builds will push the
# final images to the registry specified here.
# This makes it easy to use ECR to test changes on the pipeline before pushing to Quay.
QUAY_REGISTRY_URL = os.environ.get("QUAY_REGISTRY", "quay.io/mongodb")


@dataclass
class BuildConfiguration:
    image_type: str
    base_repository: str
    namespace: str

    include_tags: list[str]
    skip_tags: list[str]

    builder: str = "docker"
    parallel: bool = False
    architecture: Optional[List[str]] = None
    sign: bool = False

    pipeline: bool = True
    debug: bool = True

    def build_args(self, args: Optional[Dict[str, str]] = None) -> Dict[str, str]:
        if args is None:
            args = {}
        args = args.copy()

        args["registry"] = self.base_repository

        return args

    def get_skip_tags(self) -> list[str]:
        return make_list_of_str(self.skip_tags)

    def get_include_tags(self) -> list[str]:
        return make_list_of_str(self.include_tags)


def make_list_of_str(value: Union[None, str, List[str]]) -> List[str]:
    if value is None:
        return []

    if isinstance(value, str):
        return [e.strip() for e in value.split(",")]

    return value


def operator_build_configuration(
    builder: str, parallel: bool, debug: bool, architecture: Optional[List[str]] = None, sign: bool = False
) -> BuildConfiguration:
    bc = BuildConfiguration(
        image_type=os.environ.get("distro", DEFAULT_IMAGE_TYPE),
        base_repository=os.environ["BASE_REPO_URL"],
        namespace=os.environ.get("namespace", DEFAULT_NAMESPACE),
        skip_tags=make_list_of_str(os.environ.get("skip_tags")),
        include_tags=make_list_of_str(os.environ.get("include_tags")),
        builder=builder,
        parallel=parallel,
        debug=debug,
        architecture=architecture,
        sign=sign,
    )

    logger.info(f"is_running_in_patch: {is_running_in_patch()}")
    logger.info(f"is_running_in_evg_pipeline: {is_running_in_evg_pipeline()}")
    if is_running_in_patch() or not is_running_in_evg_pipeline():
        logger.info(
            f"Running build not in evg pipeline (is_running_in_evg_pipeline={is_running_in_evg_pipeline()}) "
            f"or in pipeline but not from master (is_running_in_patch={is_running_in_patch()}). "
            "Adding 'master' tag to skip to prevent publishing to the latest dev image."
        )
        bc.skip_tags.append("master")

    return bc


def is_running_in_evg_pipeline():
    return os.getenv("RUNNING_IN_EVG", "") == "true"


class MissingEnvironmentVariable(Exception):
    pass


def should_pin_at() -> Optional[Tuple[str, str]]:
    """Gets the value of the pin_tag_at to tag the images with.

    Returns its value split on :.
    """
    # We need to return something so `partition` does not raise
    # AttributeError
    is_patch = is_running_in_patch()

    try:
        pinned = os.environ["pin_tag_at"]
    except KeyError:
        raise MissingEnvironmentVariable(f"pin_tag_at environment variable does not exist, but is required")

    if is_patch:
        if pinned == "00:00":
            raise "Pinning to midnight during a patch is not supported. Please pin to another date!"

    hour, _, minute = pinned.partition(":")
    return hour, minute


def is_running_in_patch():
    is_patch = os.environ.get("is_patch")
    return is_patch is not None and is_patch.lower() == "true"


def build_id() -> str:
    """Returns the current UTC time in ISO8601 date format.

    If running in Evergreen and `created_at` expansion is defined, use the
    datetime defined in that variable instead.

    It is possible to pin this time at midnight (00:00) for periodic builds. If
    running a manual build, then the Evergreen `pin_tag_at` variable needs to be
    set to the empty string, in which case, the image tag suffix will correspond
    to the current timestamp.

    """

    date = datetime.utcnow()
    try:
        created_at = os.environ["created_at"]
        date = datetime.strptime(created_at, "%y_%m_%d_%H_%M_%S")
    except KeyError:
        pass

    hour, minute = should_pin_at()
    if hour and minute:
        logger.info(f"we are pinning to, hour: {hour}, minute: {minute}")
        date = date.replace(hour=int(hour), minute=int(minute), second=0)
    else:
        logger.warning(f"hour and minute cannot be extracted from provided pin_tag_at env, pinning to now")

    string_time = date.strftime("%Y%m%dT%H%M%SZ")

    return string_time


def get_release() -> Dict:
    with open("release.json") as release:
        return json.load(release)


def get_git_release_tag() -> tuple[str, bool]:
    release_env_var = os.getenv("triggered_by_git_tag")

    # that means we are in a release and only return the git_tag; otherwise we want to return the patch_id
    # appended to ensure the image created is unique and does not interfere
    if release_env_var is not None:
        return release_env_var, True

    patch_id = os.environ.get("version_id", "latest")
    return patch_id, False


def copy_into_container(client, src, dst):
    """Copies a local file into a running container."""

    os.chdir(os.path.dirname(src))
    srcname = os.path.basename(src)
    with tarfile.open(src + ".tar", mode="w") as tar:
        tar.add(srcname)

    name, dst = dst.split(":")
    container = client.containers.get(name)

    with open(src + ".tar", "rb") as fd:
        container.put_archive(os.path.dirname(dst), fd.read())


"""
Generates docker manifests by running the following commands:
1. Clear existing manifests
docker manifest rm config.repo_url/image:tag
2. Create the manifest
docker manifest create config.repo_url/image:tag --amend config.repo_url/image:tag-amd64 --amend config.repo_url/image:tag-arm64
3. Push the manifest
docker manifest push config.repo_url/image:tag
"""


# This method calls docker directly on the command line, this is different from the rest of the code which uses
# Sonar as an interface to docker. We decided to keep this asymmetry for now, as Sonar will be removed soon.


def create_and_push_manifest(image: str, tag: str) -> None:
    final_manifest = image + ":" + tag
    args = ["docker", "manifest", "rm", final_manifest]
    args_str = " ".join(args)
    logger.debug(f"removing existing manifest: {args_str}")
    subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    args = [
        "docker",
        "manifest",
        "create",
        final_manifest,
        "--amend",
        final_manifest + "-amd64",
        "--amend",
        final_manifest + "-arm64",
    ]
    args_str = " ".join(args)
    logger.debug(f"creating new manifest: {args_str}")
    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    if cp.returncode != 0:
        raise Exception(cp.stderr)

    args = ["docker", "manifest", "push", final_manifest]
    args_str = " ".join(args)
    logger.info(f"pushing new manifest: {args_str}")
    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    if cp.returncode != 0:
        raise Exception(cp.stderr)


def try_get_platform_data(client, image):
    """Helper function to try and retrieve platform data."""
    try:
        return client.images.get_registry_data(image)
    except Exception as e:
        logger.error("Failed to get registry data for image: {0}. Error: {1}".format(image, str(e)))
        return None


"""
Checks if a docker image supports AMD and ARM platforms by inspecting the registry data.

:param str image: The image name and tag
"""


def check_multi_arch(image: str, suffix: str) -> bool:
    client = docker.from_env()
    platforms = ["linux/amd64", "linux/arm64"]

    for img in [image, image + suffix]:
        reg_data = try_get_platform_data(client, img)
        if reg_data is not None and all(reg_data.has_platform(p) for p in platforms):
            logger.info("Base image {} supports multi architecture, building for ARM64 and AMD64".format(img))
            return True

    logger.info("Base image {} is single-arch, building only for AMD64.".format(img))
    return False


def sonar_build_image(
    image_name: str,
    build_configuration: BuildConfiguration,
    args: Dict[str, str] = None,
    inventory="inventory.yaml",
):
    """Calls sonar to build `image_name` with arguments defined in `args`."""
    build_options = {
        # Will continue building an image if it finds an error. See next comment.
        "continue_on_errors": True,
        # But will still fail after all the tasks have completed
        "fail_on_errors": True,
        "pipeline": build_configuration.pipeline,
    }

    logger.info(f"Sonar config: {build_configuration}")

    process_image(
        image_name,
        skip_tags=build_configuration.get_skip_tags(),
        include_tags=build_configuration.get_include_tags(),
        build_args=build_configuration.build_args(args),
        inventory=inventory,
        build_options=build_options,
    )

    produce_sbom(build_configuration, args)


def produce_sbom(build_configuration, args):
    if not is_running_in_evg_pipeline():
        logger.info("Skipping SBOM Generation (enabled only for EVG)")
        return

    image_pull_spec = "unknown"
    try:
        image_pull_spec = args["quay_registry"] + args["ubi_suffix"]
    except KeyError:
        logger.error(f"Could not find image pull spec. Args: {args}, BuildConfiguration: {build_configuration}")
        logger.error(f"Skipping SBOM generation")
        return

    image_tag = "unknown"
    try:
        image_tag = args["release_version"]
    except KeyError:
        logger.error(f"Could not find image tag. Args: {args}, BuildConfiguration: {build_configuration}")
        logger.error(f"Skipping SBOM generation")
        return

    image_pull_spec = f"{image_pull_spec}:{image_tag}"
    print(f"Producing SBOM for image: {image_pull_spec}")
    proc = subprocess.Popen(
        ["./scripts/evergreen/generate_upload_sbom.sh", "-i", image_pull_spec, "-b", "enterprise-operator-sboms"],
        stdout=subprocess.PIPE,
    )
    for line in io.TextIOWrapper(proc.stdout, encoding="utf-8"):
        logger.info(line.rstrip())
    # Ignoring the return code for now.


def build_tests_image(build_configuration: BuildConfiguration):
    """
    Builds image used to run tests.
    """
    image_name = "test"

    # helm directory needs to be copied over to the tests docker context.
    helm_src = "helm_chart"
    helm_dest = "docker/mongodb-enterprise-tests/helm_chart"
    requirements_dest = "docker/mongodb-enterprise-tests/requirements.txt"

    shutil.rmtree(helm_dest, ignore_errors=True)
    copy_tree(helm_src, helm_dest)
    shutil.copyfile("requirements.txt", requirements_dest)

    sonar_build_image(image_name, build_configuration, {}, "inventories/test.yaml")


def build_operator_image(build_configuration: BuildConfiguration):
    """Calculates arguments required to build the operator image, and starts the build process."""
    # In evergreen we can pass test_suffix env to publish the operator to a quay
    # repostory with a given suffix.
    test_suffix = os.environ.get("test_suffix", "")
    log_automation_config_diff = os.environ.get("LOG_AUTOMATION_CONFIG_DIFF", "false")
    version, _ = get_git_release_tag()
    args = {
        "version": version,
        "log_automation_config_diff": log_automation_config_diff,
        "test_suffix": test_suffix,
        "debug": build_configuration.debug,
    }

    logger.info(f"Building Operator args: {args}")

    build_image_generic(build_configuration, "operator", "inventory.yaml", args)


def build_database_image(build_configuration: BuildConfiguration):
    """
    Builds a new database image.
    """
    release = get_release()
    version = release["databaseImageVersion"]
    args = {"version": version}
    build_image_generic(build_configuration, "database", "inventories/database.yaml", args)


def build_operator_image_patch(build_configuration: BuildConfiguration):
    """This function builds the operator locally and pushed into an existing
    Docker image. This is the fastest way I could image we can do this."""

    client = docker.from_env()
    # image that we know is where we build operator.
    image_repo = (
        build_configuration.base_repository + "/" + build_configuration.image_type + "/mongodb-enterprise-operator"
    )
    image_tag = "latest"
    repo_tag = image_repo + ":" + image_tag

    logger.debug("Pulling image:", repo_tag)
    try:
        image = client.images.get(repo_tag)
    except docker.errors.ImageNotFound:
        logger.debug("Operator image does not exist locally. Building it now")
        build_operator_image(build_configuration)
        return

    logger.debug("Done")
    too_old = datetime.now() - timedelta(hours=3)
    image_timestamp = datetime.fromtimestamp(
        image.history()[0]["Created"]
    )  # Layer 0 is the latest added layer to this Docker image. [-1] is the FROM layer.

    if image_timestamp < too_old:
        logger.info("Current operator image is too old, will rebuild it completely first")
        build_operator_image(build_configuration)
        return

    container_name = "mongodb-enterprise-operator"
    operator_binary_location = "/usr/local/bin/mongodb-enterprise-operator"
    try:
        client.containers.get(container_name).remove()
        logger.debug(f"Removed {container_name}")
    except docker.errors.NotFound:
        pass

    container = client.containers.run(repo_tag, name=container_name, entrypoint="sh", detach=True)

    logger.debug("Building operator with debugging symbols")
    subprocess.run(["make", "manager"], check=True, stdout=subprocess.PIPE)
    logger.debug("Done building the operator")

    copy_into_container(
        client,
        os.getcwd() + "/docker/mongodb-enterprise-operator/content/mongodb-enterprise-operator",
        container_name + ":" + operator_binary_location,
    )

    # Commit changes on disk as a tag
    container.commit(
        repository=image_repo,
        tag=image_tag,
    )
    # Stop this container so we can use it next time
    container.stop()
    container.remove()

    logger.info("Pushing operator to {}:{}".format(image_repo, image_tag))
    client.images.push(
        repository=image_repo,
        tag=image_tag,
    )


def get_supported_variants_for_image(image: str) -> List[str]:
    return get_release()["supportedImages"][image]["variants"]


def image_config(
    image_name: str,
    name_prefix: str = "mongodb-enterprise-",
    s3_bucket: str = "enterprise-operator-dockerfiles",
    ubi_suffix: str = "-ubi",
    base_suffix: str = "",
) -> Tuple[str, Dict[str, str]]:
    """Generates configuration for an image suitable to be passed
    to Sonar.

    It returns a dictionary with registries and S3 configuration."""
    args = {
        "quay_registry": "{}/{}{}".format(QUAY_REGISTRY_URL, name_prefix, image_name),
        "ecr_registry_ubi": "268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/{}{}".format(
            name_prefix, image_name
        ),
        "s3_bucket_http": "https://{}.s3.amazonaws.com/dockerfiles/{}{}".format(s3_bucket, name_prefix, image_name),
        "ubi_suffix": ubi_suffix,
        "base_suffix": base_suffix,
    }

    return image_name, args


def args_for_daily_image(image_name: str) -> Dict[str, str]:
    """Returns configuration for an image to be able to be pushed with Sonar.

    This includes the quay_registry and ospid corresponding to RedHat's project id.
    """
    image_configs = [
        image_config("appdb"),
        image_config("database"),
        image_config("init-appdb"),
        image_config("agent"),
        image_config("init-database"),
        image_config("init-ops-manager"),
        image_config("operator"),
        image_config("ops-manager"),
        image_config("mongodb-agent", name_prefix="", ubi_suffix="-ubi", base_suffix="-ubi"),
        image_config(
            image_name="mongodb-kubernetes-operator",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
            # community ubi image does not have a suffix in its name
            ubi_suffix="",
        ),
        image_config(
            image_name="mongodb-kubernetes-readinessprobe",
            ubi_suffix="",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
        ),
        image_config(
            image_name="mongodb-kubernetes-operator-version-upgrade-post-start-hook",
            ubi_suffix="",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
        ),
    ]

    images = {k: v for k, v in image_configs}
    return images[image_name]


def is_version_in_range(version: str, min_version: str, max_version: str) -> bool:
    """Check if version is in the range"""
    try:
        version_without_rc = semver.finalize_version(version)
    except ValueError:
        version_without_rc = version
    if min_version and max_version:
        # Greater or equal for lower bound, strictly lower for upper bound
        return semver.compare(min_version, version_without_rc) <= 0 > semver.compare(version_without_rc, max_version)
    return True


"""
Starts the daily build process for an image. This function works for all images we support, for community and 
enterprise operator. The list of supported image_name is defined in get_builder_function_for_image_name.
Builds an image for each version listed in ./release.json
The registry used to pull base image and output the daily build is configured in the image_config function, it is passed
as an argument to the inventories/daily.yaml file.

If the context image supports both ARM and AMD architectures, both will be built.
"""


def build_image_daily(
    image_name: str,
    min_version: str = None,
    max_version: str = None,
):
    """Builds a daily image."""

    def get_architectures_set(build_configuration, args):
        """Determine the set of architectures to build for"""
        arch_set = set(build_configuration.architecture) if build_configuration.architecture else set()
        if arch_set == {"arm64"}:
            raise ValueError("Building for ARM64 only is not supported yet")

        # Automatic architecture detection is the default behavior if 'arch' argument isn't specified
        if arch_set == set():
            if check_multi_arch(
                image=args["quay_registry"] + args["ubi_suffix"] + ":" + args["release_version"],
                suffix="-context",
            ):
                arch_set = {"amd64", "arm64"}
            else:
                # When nothing specified and single-arch, default to amd64
                arch_set = {"amd64"}

        return arch_set

    def create_and_push_manifests(args: dict):
        """Create and push manifests for all registries."""
        registries = [args["ecr_registry_ubi"], args["quay_registry"]]
        tags = [args["release_version"], args["release_version"] + "-b" + args["build_id"]]
        for registry in registries:
            for tag in tags:
                create_and_push_manifest(registry + args["ubi_suffix"], tag)

    def inner(build_configuration: BuildConfiguration):
        supported_versions = get_supported_version_for_image_matrix_handling(image_name)
        variants = get_supported_variants_for_image(image_name)

        args = args_for_daily_image(image_name)
        args["build_id"] = build_id()
        logger.info("Supported Versions for {}: {}".format(image_name, supported_versions))

        completed_versions = set()
        for version in filter(lambda x: is_version_in_range(x, min_version, max_version), supported_versions):
            build_configuration = copy.deepcopy(build_configuration)
            if build_configuration.include_tags is None:
                build_configuration.include_tags = []
            build_configuration.include_tags.extend(variants)

            logger.info("Rebuilding {} with variants {}".format(version, variants))
            args["release_version"] = version

            arch_set = get_architectures_set(build_configuration, args)

            if version not in completed_versions:
                if arch_set == {"amd64", "arm64"}:
                    for arch in arch_set:
                        # Suffix to append to images name for multi-arch (see usage in daily.yaml inventory)
                        args["architecture_suffix"] = f"-{arch}"
                        args["platform"] = arch
                        sonar_build_image(
                            "image-daily-build",
                            build_configuration,
                            args,
                            inventory="inventories/daily.yaml",
                        )
                        if build_configuration.sign:
                            sign_image_in_repositories(args, arch)
                    create_and_push_manifests(args)
                    if build_configuration.sign:
                        sign_image_in_repositories(args)
                else:
                    # No suffix for single arch images
                    args["architecture_suffix"] = ""
                    args["platform"] = "amd64"
                    sonar_build_image(
                        "image-daily-build",
                        build_configuration,
                        args,
                        inventory="inventories/daily.yaml",
                    )
                    if build_configuration.sign:
                        sign_image_in_repositories(args)
                completed_versions.add(version)

    return inner


def sign_image_in_repositories(args: Dict[str, str], arch: str = None):
    repositories = [args["ecr_registry_ubi"] + args["ubi_suffix"], args["quay_registry"] + args["ubi_suffix"]]
    tag = args["release_version"]
    if arch:
        tag = f"{tag}-{arch}"

    for repository in repositories:
        sign_image(repository, tag)
        verify_signature(repository, tag)


def find_om_in_releases(om_version: str, releases: Dict[str, str]) -> Optional[str]:
    """
    There are a few alternatives out there that allow for json-path or xpath-type
    traversal of Json objects in Python, I don't have time to look for one of
    them now but I have to do at some point.
    """
    for release in releases:
        if release["version"] == om_version:
            for platform in release["platform"]:
                if platform["package_format"] == "deb" and platform["arch"] == "x86_64":
                    for package in platform["packages"]["links"]:
                        if package["name"] == "tar.gz":
                            return package["download_link"]
    return None


def get_om_releases() -> Dict[str, str]:
    """Returns a dictionary representation of the Json document holdin all the OM
    releases.
    """
    ops_manager_release_archive = (
        "https://info-mongodb-com.s3.amazonaws.com/com-download-center/ops_manager_release_archive.json"
    )

    return requests.get(ops_manager_release_archive).json()


def find_om_url(om_version: str) -> str:
    """Gets a download URL for a given version of OM."""
    releases = get_om_releases()

    current_release = find_om_in_releases(om_version, releases["currentReleases"])
    if current_release is None:
        current_release = find_om_in_releases(om_version, releases["oldReleases"])

    if current_release is None:
        raise ValueError("Ops Manager version {} could not be found".format(om_version))

    return current_release


def build_init_om_image(build_configuration: BuildConfiguration):
    release = get_release()
    init_om_version = release["initOpsManagerVersion"]
    args = {"version": init_om_version}
    build_image_generic(build_configuration, "init-ops-manager", "inventories/init_om.yaml", args)


def build_om_image(build_configuration: BuildConfiguration):
    # Make this a parameter for the Evergreen build
    # https://github.com/evergreen-ci/evergreen/wiki/Parameterized-Builds
    om_version = os.environ.get("om_version")
    if om_version is None:
        raise ValueError("`om_version` should be defined.")

    om_download_url = os.environ.get("om_download_url", "")
    if om_download_url == "":
        om_download_url = find_om_url(om_version)

    args = {
        "version": om_version,
        "om_download_url": om_download_url,
    }
    build_image_generic(build_configuration, "ops-manager", "inventories/om.yaml", args)


def build_image_generic(
    config: BuildConfiguration,
    image_name: str,
    inventory_file: str,
    extra_args: dict = None,
    registry_address: str = None,
):
    args = extra_args or {}
    version = extra_args.get("version", "")
    registry = f"{QUAY_REGISTRY_URL}/mongodb-enterprise-{image_name}" if not registry_address else registry_address
    args["quay_registry"] = registry

    sonar_build_image(image_name, config, args, inventory_file)
    if config.sign and is_release_step_executed(config.get_skip_tags(), config.get_include_tags()):
        sign_image(registry, version + "-context")
        verify_signature(registry, version + "-context")


def is_release_step_executed(skip_tags: List[str], include_tags: List[str]) -> bool:
    return "release" not in skip_tags and (not include_tags or ("release" in include_tags))


def build_init_appdb(build_configuration: BuildConfiguration):
    release = get_release()
    version = release["initAppDbVersion"]
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": version, "is_appdb": True, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}
    build_image_generic(build_configuration, "init-appdb", "inventories/init_appdb.yaml", args)


def build_agent_in_sonar(
    build_configuration: BuildConfiguration,
    image_version,
    init_database_image,
    mongodb_tools_url_ubi,
    mongodb_agent_url_ubi: str,
):
    args = {
        "version": image_version,
        "mongodb_tools_url_ubi": mongodb_tools_url_ubi,
        "mongodb_agent_url_ubi": mongodb_agent_url_ubi,
        "init_database_image": init_database_image,
    }

    registry = QUAY_REGISTRY_URL + f"/mongodb-agent-ubi"
    args["quay_registry"] = registry

    build_image_generic(
        config=build_configuration,
        image_name="agent",
        inventory_file="inventories/agent.yaml",
        extra_args=args,
        registry_address=registry,
    )
    # Agent is the only image for which release is part of the inventory, on top of -context release
    # This is done usually by daily builds
    if build_configuration.sign and is_release_step_executed(
        build_configuration.get_skip_tags(), build_configuration.get_include_tags()
    ):
        sign_image(registry, image_version)
        verify_signature(registry, image_version)


def build_agent_default_case(build_configuration: BuildConfiguration):
    """
    Build the agent for the latest operator for patches and operator releases
    """
    release = get_release()

    agent_versions_to_build = build_agent_gather_versions(release)

    operator_version, is_release = get_git_release_tag()

    logger.info(f"Building Agent versions: {agent_versions_to_build} for Operator versions: {operator_version}")

    tasks_queue = Queue()
    with ProcessPoolExecutor(max_workers=1 if build_configuration.parallel is False else None) as executor:
        for agent_version in agent_versions_to_build:
            _build_agent(agent_version, build_configuration, executor, operator_version, tasks_queue, is_release)

    exceptions_found = False
    for task in tasks_queue.queue:
        if task.exception() is not None:
            exceptions_found = True
            logger.fatal(f"The following exception has been found when building: {task.exception()}")
    if exceptions_found:
        raise Exception(
            f"Exception(s) found when processing Agent images. \nSee also previous logs for more info\nFailing the build"
        )


def build_agent_on_agent_bump(build_configuration: BuildConfiguration):
    """
    Build the agent matrix (operator version x agent version), triggered by PCT
    """
    release = get_release()

    agent_versions_to_build = build_agent_gather_versions(release)
    min_supported_version_operator_for_static = "1.25.0"
    supported_operator_versions = [
        v
        for v in get_release()["supportedImages"]["operator"]["versions"]
        if v >= min_supported_version_operator_for_static
    ]

    tasks_queue = Queue()
    with ProcessPoolExecutor(max_workers=1 if build_configuration.parallel is False else None) as executor:
        for operator_version in supported_operator_versions:
            logger.info(f"Building Agent versions: {agent_versions_to_build} for Operator versions: {operator_version}")
            for agent_version in agent_versions_to_build:
                _build_agent(agent_version, build_configuration, executor, operator_version, tasks_queue, True)

    exceptions_found = False
    for task in tasks_queue.queue:
        if task.exception() is not None:
            exceptions_found = True
            logger.fatal(f"The following exception has been found when building: {task.exception()}")
    if exceptions_found:
        raise Exception(
            f"Exception(s) found when processing Agent images. \nSee also previous logs for more info\nFailing the build"
        )


def _build_agent(
    agent_version: Tuple[str, str],
    build_configuration: BuildConfiguration,
    executor: ProcessPoolExecutor,
    operator_version: str,
    tasks_queue: Queue,
    use_quay: bool = False,
):
    agent_distro = "rhel7_x86_64"
    tools_version = agent_version[1]
    tools_distro = "rhel70-x86_64"
    image_version = f"{agent_version[0]}_{operator_version}"
    mongodb_tools_url_ubi = (
        f"https://downloads.mongodb.org/tools/db/mongodb-database-tools-{tools_distro}-{tools_version}.tgz"
    )
    mongodb_agent_url_ubi = f"https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-{agent_version[0]}.{agent_distro}.tar.gz"
    # We use Quay if not in a patch
    # We could rely on input params (quay_registry or registry), but it makes templating more complex in the inventory
    non_quay_registry = os.environ.get("REGISTRY", "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev")
    base_init_database_repo = QUAY_REGISTRY_URL if use_quay else non_quay_registry
    init_database_image = f"{base_init_database_repo}/mongodb-enterprise-init-database-ubi:{operator_version}"

    tasks_queue.put(
        executor.submit(
            build_agent_in_sonar,
            build_configuration,
            image_version,
            init_database_image,
            mongodb_tools_url_ubi,
            mongodb_agent_url_ubi,
        )
    )


def build_agent_gather_versions(release: Dict[str, str]) -> List[Tuple[str, str]]:
    # This is a list of a tuples - agent version and corresponding tools version
    agent_versions_to_build = list()
    agent_versions_to_build.append(
        (
            release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"],
            release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager_tools"],
        )
    )
    for _, om in release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"].items():
        agent_versions_to_build.append((om["agent_version"], om["tools_version"]))

    return agent_versions_to_build


def get_builder_function_for_image_name() -> Dict[str, Callable]:
    """Returns a dictionary of image names that can be built."""

    return {
        "test": build_tests_image,
        "operator": build_operator_image,
        "operator-quick": build_operator_image_patch,
        "database": build_database_image,
        "agent-pct": build_agent_on_agent_bump,
        "agent": build_agent_default_case,
        #
        # Init images
        "init-appdb": build_init_appdb,
        "init-database": build_init_database,
        "init-ops-manager": build_init_om_image,
        #
        # Daily builds
        "operator-daily": build_image_daily("operator"),
        "appdb-daily": build_image_daily("appdb"),
        "database-daily": build_image_daily("database"),
        "init-appdb-daily": build_image_daily("init-appdb"),
        "init-database-daily": build_image_daily("init-database"),
        "init-ops-manager-daily": build_image_daily("init-ops-manager"),
        "ops-manager-6-daily": build_image_daily("ops-manager", min_version="6.0.0", max_version="7.0.0"),
        "ops-manager-7-daily": build_image_daily("ops-manager", min_version="7.0.0", max_version="8.0.0"),
        #
        # Ops Manager image
        "ops-manager": build_om_image,
        #
        # Community images
        "mongodb-agent-daily": build_image_daily("mongodb-agent"),
        "mongodb-kubernetes-readinessprobe-daily": build_image_daily(
            "mongodb-kubernetes-readinessprobe",
        ),
        "mongodb-kubernetes-operator-version-upgrade-post-start-hook-daily": build_image_daily(
            "mongodb-kubernetes-operator-version-upgrade-post-start-hook",
        ),
        "mongodb-kubernetes-operator-daily": build_image_daily("mongodb-kubernetes-operator"),
    }


# TODO: nam static: remove this once static containers becomes the default
def build_init_database(build_configuration: BuildConfiguration):
    release = get_release()
    version = release["initDatabaseVersion"]  # comes from release.json
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi, "is_appdb": False}
    build_image_generic(build_configuration, "init-database", "inventories/init_database.yaml", args)


def build_image(image_name: str, build_configuration: BuildConfiguration):
    """Builds one of the supported images by its name."""
    get_builder_function_for_image_name()[image_name](build_configuration)


def build_all_images(
    images: Iterable[str],
    builder: str,
    debug: bool = False,
    parallel: bool = False,
    architecture: Optional[List[str]] = None,
    sign: bool = False,
):
    """Builds all the images in the `images` list."""
    build_configuration = operator_build_configuration(builder, parallel, debug, architecture, sign)
    if sign:
        mongodb_artifactory_login()
    for image in images:
        build_image(image, build_configuration)


def calculate_images_to_build(
    images: List[str], include: Optional[List[str]], exclude: Optional[List[str]]
) -> Set[str]:
    """
    Calculates which images to build based on the `images`, `include` and `exclude` sets.

    >>> calculate_images_to_build(["a", "b"], ["a"], ["b"])
    ... ["a"]
    """

    if not include and not exclude:
        return set(images)
    include = set(include or [])
    exclude = set(exclude or [])
    images = set(images or [])

    for image in include.union(exclude):
        if image not in images:
            raise ValueError("Image definition {} not found".format(image))

    images_to_build = include.intersection(images)
    if exclude:
        images_to_build = images.difference(exclude)
    return images_to_build


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--include", action="append")
    parser.add_argument("--exclude", action="append")
    parser.add_argument("--builder", default="docker", type=str)
    parser.add_argument("--list-images", action="store_true")
    parser.add_argument("--parallel", action="store_true", default=False)
    parser.add_argument("--debug", action="store_true", default=False)
    parser.add_argument(
        "--arch",
        choices=["amd64", "arm64"],
        nargs="+",
        help="for daily builds only, specify the list of architectures to build for images",
    )
    parser.add_argument("--sign", action="store_true", default=False)
    args = parser.parse_args()

    if args.list_images:
        print(get_builder_function_for_image_name().keys())
        sys.exit(0)

    if args.arch == ["arm64"]:
        print("Building for arm64 only is not supported yet")
        sys.exit(1)

    if not args.sign:
        logger.warning("--sign flag not provided, images won't be signed")

    images_to_build = calculate_images_to_build(
        list(get_builder_function_for_image_name().keys()), args.include, args.exclude
    )

    build_all_images(
        images_to_build, args.builder, debug=args.debug, parallel=args.parallel, architecture=args.arch, sign=args.sign
    )


if __name__ == "__main__":
    main()
