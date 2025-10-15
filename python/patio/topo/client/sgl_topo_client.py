# -*- coding: utf-8 -*-
# @Author: zibai.gj

from typing import Optional

from patio.logger import init_logger
from patio.topo.client.base_topo_client import GroupTopoClient

logger = init_logger(__name__)


class SGLangGroupTopoClient(GroupTopoClient):
    _instance = None

    # Singleton
    def __new__(cls, *args, **kwargs):
        if cls._instance is None:
            cls._instance = super(GroupTopoClient, cls).__new__(cls, *args, **kwargs)
            cls._instance.__initialized = False
        return cls._instance

    def __init__(self):
        if not self.__initialized:
            self.worker_endpoint = None
            self.__initialized = True

    def register(self, url: str, worker_dict: dict, file_path: Optional[str] = None) -> bool:
        raise NotImplementedError

    def unregister(self):
        raise NotImplementedError
