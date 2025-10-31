# -*- coding: utf-8 -*-
# @Author: zibai.gj
import json
import os
import traceback

import requests
from argparse import ArgumentError
from http.client import InvalidURL
from typing import Optional

from patio.logger import init_logger
from patio.topo.client.base_topo_client import GroupTopoClient
from patio.topo import utils

logger = init_logger(__name__)


def get_sgl_router_url() -> Optional[str]:
    rbg_group_name = os.getenv("GROUP_NAME")
    if rbg_group_name is None:
        raise Exception("GROUP_NAME is not set")

    router_role_name = os.getenv("SGL_ROUTER_ROLE_NAME")
    if router_role_name is None:
        raise Exception("SGL_ROUTER_ROLE_NAME is not set")

    return f"{rbg_group_name}-{router_role_name}-0.s-{rbg_group_name}-{router_role_name}"

def get_worker_url() -> Optional[str]:
    rbg_group_name = os.getenv("GROUP_NAME")
    if rbg_group_name is None:
        raise Exception("GROUP_NAME is not set")

    role_name = os.getenv("ROLE_NAME")
    if role_name is None:
        raise Exception("ROLE_NAME is not set")

    role_index = os.getenv("ROLE_INDEX")
    if role_index is None:
        raise Exception("ROLE_INDEX is not set")

    return f"{rbg_group_name}-{role_name}-{role_index}.s-{rbg_group_name}-{role_name}"


def translate_http_response_parameters(raw: str) -> str:
    return raw.replace("/", "%2F")

class SGLangGroupTopoClient(GroupTopoClient):
    _instance = None

    # Singleton
    def __new__(cls, *args, **kwargs):
        if cls._instance is None:
            cls._instance = super(SGLangGroupTopoClient, cls).__new__(cls, *args, **kwargs)
            cls._instance.__initialized = False
        return cls._instance

    def __init__(self):
        if not self.__initialized:
            self.worker_endpoint = None
            self.__initialized = True

    def register(self, url: str, worker_dict: dict, file_path: Optional[str] = None) -> bool:
        """
        worker_dict example:
            topo_type: sglang
            port: 8000

        """
        sgl_router_url = get_sgl_router_url()
        if sgl_router_url is None:
            raise Exception("failed to find SGLang router's URL, please set env SGL_ROUTER_URL.")

        sgl_router_port = os.getenv("SGL_ROUTER_PORT")
        if sgl_router_port is None:
            raise Exception("SGL_ROUTER_PORT is not set")

        worker_info_payload = worker_dict["data"] if "data" in worker_dict else None
        if worker_info_payload is None:
            raise Exception(f"failed to find worker's info, please add worker's info to worker_dict.")

        port = worker_info_payload["port"] if "port" in worker_info_payload else None
        if port is None:
            raise Exception("failed to find inference engine's port, please set it in worker_dict.")

        url = f"http://{get_worker_url()}:{port}"
        worker_info_payload["url"] = url
        del worker_info_payload["port"]


        # self.worker_endpoint = worker_info_payload["url"] if "url" in worker_info_payload else None
        # if self.worker_endpoint is None:
        #     raise Exception(f"failed to find worker's info, please ensure 'url' is set in worker_dict.")

        # TODO: Validate required fields in worker_info_payload and fail fast

        def f():
            worker_registration_url = f"http://{sgl_router_url}:{sgl_router_port}/workers"
            worker_info_json = json.dumps(worker_info_payload)
            resp = requests.post(worker_registration_url, json=worker_info_payload, headers={"Content-Type": "application/json"})
            if resp.status_code == 202:
                # Status Code 202 Accepted
                self.worker_endpoint = worker_info_payload["url"]
                logger.info(f"registered worker successfully.")
            else:
                raise Exception(f"register failed, url: {worker_registration_url}, status_code: {resp.status_code}, content: {resp.text}")

        try:
            utils.retry(f, retry_times=60, interval=3)
            return True
        except Exception as e:
            logger.error(f"failed to register worker: {e}")
            traceback.print_exc()
            return False


    def unregister(self):
        sgl_router_url = get_sgl_router_url()
        if sgl_router_url is None:
            raise Exception("failed to find SGLang router's URL, please set env SGL_ROUTER_URL.")

        sgl_router_port = os.getenv("SGL_ROUTER_PORT")
        if sgl_router_port is None:
            raise Exception("SGL_ROUTER_PORT is not set")


        def f():
            worker_registration_url = f"http://{sgl_router_url}:{sgl_router_port}/workers/{translate_http_response_parameters(self.worker_endpoint)}"
            resp = requests.delete(worker_registration_url)
            if resp.status_code == 202:
                # Status Code 202 Accepted
                logger.info(f"unregistered worker successfully.")
            else:
                raise Exception(
                    f"unregister failed, url: {worker_registration_url}, status_code: {resp.status_code}, content: {resp.text}")

        try:
            utils.retry(f, retry_times=60, interval=3)
            return True
        except Exception as e:
            logger.error(f"failed to unregister worker: {e}")
            traceback.print_exc()
            return False
