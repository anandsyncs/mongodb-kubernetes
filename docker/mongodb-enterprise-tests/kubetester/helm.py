import logging
import os
import re
import subprocess
import uuid
from typing import Dict, List, Optional, Tuple


def helm_template(
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    templates: Optional[str] = None,
    helm_options: Optional[List[str]] = None,
) -> str:
    """generates yaml file using Helm and returns its name. Provide 'templates' if you need to run
    a specific template from the helm chart"""
    command_args = _create_helm_args(helm_args, helm_options)

    if templates is not None:
        command_args.append("--show-only")
        command_args.append(templates)

    args = ("helm", "template", *(command_args), _helm_chart_dir(helm_chart_path))
    logging.info(args)

    yaml_file_name = "{}.yaml".format(str(uuid.uuid4()))
    with open(yaml_file_name, "w") as output:
        process_run_and_check(" ".join(args), stdout=output, check=True, shell=True)

    return yaml_file_name


def helm_install(
    name: str,
    namespace: str,
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    helm_options: Optional[List[str]] = None,
):
    command_args = _create_helm_args(helm_args, helm_options)
    args = (
        "helm",
        "upgrade",
        "--install",
        f"--namespace={namespace}",
        *(command_args),
        name,
        _helm_chart_dir(helm_chart_path),
    )
    logging.info(args)

    process_run_and_check(" ".join(args), check=True, capture_output=True, shell=True)


def helm_install_from_chart(
    namespace: str,
    release: str,
    chart: str,
    version: str = "",
    custom_repo: Tuple[str, str] = ("stable", "https://charts.helm.sh/stable"),
    helm_args: Optional[Dict[str, str]] = None,
    override_path: Optional[str] = None,
):
    """Installs a helm chart from a repo. It can accept a new custom_repo to add before the
    chart is installed. Also, `helm_args` accepts a dictionary that will be passed as --set
    arguments to `helm install`.

    Some charts are clusterwide (like CertManager), and simultaneous installation can
    fail. This function tolerates errors when installing the Chart if `stderr` of the
    Helm process has the "release: already exists" string on it.
    """

    args = [
        "helm",
        "upgrade",
        "--install",
        release,
        f"--namespace={namespace}",
        chart,
    ]

    if override_path is not None:
        args.extend(["-f", f"{override_path}"])

    if version != "":
        args.append("--version=" + version)

    if helm_args is not None:
        args += _create_helm_args(helm_args)

    helm_repo_add(custom_repo[0], custom_repo[1])

    try:
        # In shared clusters (Kops: e2e) multiple simultaneous cert-manager
        # installations will fail. We tolerate errors in those cases.
        process_run_and_check(args, capture_output=True)
    except subprocess.CalledProcessError as exc:
        stderr = exc.stderr.decode("utf-8")
        if (
            "release: already exists" in stderr
            or "Error: UPGRADE FAILED: another operation" in stderr
        ):
            logging.info(f"Helm chart '{chart}' already installed in cluster.")
        else:
            raise


def helm_repo_add(repo_name: str, url: str):
    """
    Adds a new repo to Helm.
    """
    helm_repo_add = f"helm repo add {repo_name} {url}".split()
    logging.info(helm_repo_add)
    process_run_and_check(helm_repo_add, capture_output=True)


def process_run_and_check(args, **kwargs):
    try:
        completed_process = subprocess.run(args, **kwargs)
        completed_process.check_returncode()
    except subprocess.CalledProcessError as exc:
        stdout = exc.stdout.decode("utf-8")
        stderr = exc.stderr.decode("utf-8")
        logging.info(exc.output)
        logging.info(stdout)
        logging.info(stderr)
        raise


def helm_upgrade(
    name: str,
    namespace: str,
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    helm_options: Optional[List[str]] = None,
    helm_override_path: Optional[bool] = False,
):
    command_args = _create_helm_args(helm_args, helm_options)
    args = [
        "helm",
        "upgrade",
        "--install",
        f"--namespace={namespace}",
        *command_args,
        name,
    ]
    if helm_override_path:
        args.append(helm_chart_path)
    else:
        args.append(_helm_chart_dir(helm_chart_path))

    logging.info(args)

    process_run_and_check(" ".join(args), check=True, capture_output=True, shell=True)


def helm_uninstall(name):
    args = ("helm", "uninstall", name)
    logging.info(args)
    process_run_and_check(" ".join(args), check=True, capture_output=True, shell=True)


def _create_helm_args(
    helm_args: Dict[str, str], helm_options: Optional[List[str]] = None
) -> List[str]:
    command_args = []
    for key, value in helm_args.items():
        command_args.append("--set")

        if "," in value:
            if not re.match("^{.+}$", value):
                # Commas in values, but no lists, should be escaped
                value = value.replace(",", "\,")

            # and when commas are present, we should quote "key=value"
            key = '"' + key
            value = value + '"'

        command_args.append("{}={}".format(key, value))

    if "useRunningOperator" in helm_args:
        logging.info("Operator will not be installed this time, passing --dry-run")
        command_args.append("--dry-run")

    command_args.append("--create-namespace")

    if helm_options:
        command_args.extend(helm_options)

    return command_args


def _helm_chart_dir(default: Optional[str] = "helm_chart") -> str:
    return os.environ.get("HELM_CHART_DIR", default)
