# -*- coding: utf-8 -*-
# @Author: zibai.gj

import errno
import json
import os
import socket
import time
from typing import Optional

import yaml

from patio.envs import ROLE_INDEX, GROUP_NAME, ROLE_NAME, TOPO_CONFIG_FILE
from patio.logger import init_logger

logger = init_logger(__name__)


def get_worker_endpoint():
    # if a container has ROLE_INDEX env, it means it's a sts or lws workload.
    if ROLE_INDEX is None:
        return _get_worker_ip_address()
    else:
        return _get_worker_hostname()


def _get_worker_ip_address(address="8.8.8.8:53"):
    """IP address by which the local worker can be reached *from* the `address`.

    Args:
        address: The IP address and port of any known live service on the
            network you care about.

    Returns:
        The IP address by which the local worker can be reached from the address.
    """
    if os.getenv("POD_IP") is not None:
        return os.getenv("POD_IP")

    ip_address, port = address.split(":")
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        # This command will raise an exception if there is no internet
        # connection.
        s.connect((ip_address, int(port)))
        worker_ip_address = s.getsockname()[0]
    except OSError as e:
        worker_ip_address = "127.0.0.1"
        # [Errno 101] Network is unreachable
        if e.errno == errno.ENETUNREACH:
            try:
                # try to get worker ip address from host name
                host_name = socket.getfqdn(socket.gethostname())
                worker_ip_address = socket.gethostbyname(host_name)
            except Exception:
                pass
    finally:
        s.close()

    return worker_ip_address


def _get_worker_hostname():
    if GROUP_NAME is None or ROLE_NAME is None or ROLE_INDEX is None:
        return "unknown"
    return f"{GROUP_NAME}-{ROLE_NAME}-{ROLE_INDEX}.{GROUP_NAME}-{ROLE_NAME}"


def write_config_file(file_type: str, file_info, file_path: Optional[str] = None):
    if file_type not in ["yaml", "json"]:
        logger.warning("file type is not yaml or json, skip write file")
        return

    if file_path is None:
        file_path = TOPO_CONFIG_FILE
    logger.info(f"use default topo config file path: {file_path}")

    directory = os.path.dirname(file_path)
    if not os.path.exists(directory):
        os.makedirs(directory)

    if file_type == "yaml":
        with open(file_path, "w") as f:
            yaml.dump(file_info, f, default_flow_style=False)
            logger.info(f"Group config file has been written to {file_path}")
            return
    elif file_type == "json":
        with open(file_path, "w") as f:
            json.dump(file_info, f, ensure_ascii=False, indent=4)
            logger.info(f"Group config file has been written to {file_path}")
            return


def retry(func, args=(), kwargs=None, retry_times=3, interval=1):
    """
    Retries a function if it raises an exception.

    Args:
        func (callable): The function to call.
        args (tuple, optional): Positional arguments to pass to func. Defaults to an empty tuple.
        kwargs (dict, optional): Keyword arguments to pass to func. Defaults to None.
                                 If None, an empty dictionary is used.
        retry_times (int): The maximum number of times to attempt the function call.
                           (e.g., retry_times=3 means 1 initial attempt + 2 retries).
                           Must be 1 or greater.
        interval (int | float): The time in seconds to wait between retries.

    Returns:
        Any: The result of func if it succeeds.

    Raises:
        Exception: The last exception raised by func if all retry attempts fail.
                   This is more informative than returning False.
    """
    if kwargs is None:
        kwargs = {}
    if retry_times < 1:
        retry_times = 1
    if interval < 1:
        interval = 1

    last_exception = None  # To store the last exception if all retries fail

    for i in range(retry_times):
        try:
            # Unpack positional arguments (*args) and keyword arguments (**kwargs)
            return func(*args, **kwargs)
        except Exception as e:
            last_exception = e  # Store the exception
            attempt_num = i + 1
            logger.warning(
                f"Attempt {attempt_num}/{retry_times} for '{func.__name__}' failed. "
                f"Error: {e}"
            )
            if attempt_num < retry_times:  # Only sleep if more retries are pending
                time.sleep(interval)
            else:
                # This was the last attempt, no need to sleep
                pass

    # If the loop finishes, it means all attempts failed
    if last_exception:
        logger.error(
            f"All {retry_times} attempts for '{func.__name__}' failed. "
            f"Last error: {last_exception}"
        )
        raise last_exception  # Re-raise the last exception for the caller to handle
    else:
        # This branch should ideally not be reached if func always raises on failure
        # and retry_times > 0.
        # It could be reached if retry_times is 0 (though we validate > 0)
        # or if func somehow 'fails' without raising an Exception.
        # For robustness, we could raise a generic error or return a specific sentinel.
        # For now, consistent with raising if last_exception exists.
        raise RuntimeError("Unexpected state in retry function: No exception recorded despite failure.")
