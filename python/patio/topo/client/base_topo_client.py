# -*- coding: utf-8 -*-
# @Author: zibai.gj
import time
import traceback
from abc import ABC, abstractmethod

from typing import Optional
from urllib.parse import urljoin

from patio import envs
from patio.config import EXIT_EVENT
from patio.envs import HEARTBEAT_INTERVAL
from patio.logger import init_logger

logger = init_logger(__name__)


class GroupTopoClient(ABC):

    @abstractmethod
    def wait_engine_ready(self, worker_info: dict) -> bool:
        raise NotImplementedError

    @abstractmethod
    def register(self, url: str, worker_info: dict, file_path: Optional[str] = None) -> bool:
        raise NotImplementedError

    @abstractmethod
    def unregister(self):
        raise NotImplementedError

    def send_heartbeat(self, worker_dict: dict):
        """
        send_heartbeat: worker side service, client -> server heartbeat, regularly register its own information to scheduler
        """
        register_url = urljoin(envs.get_topo_register_endpoint(), "/topo/register")
        while not EXIT_EVENT.is_set():
            # Sleep first to wait for server ready
            time.sleep(HEARTBEAT_INTERVAL)
            try:
                payload = worker_dict["data"] if "data" in worker_dict else None
                success = self.register(register_url, payload)
                if success:
                    logger.debug(
                        f"topo client sends heartbeat successfully. register_url: {register_url}, data: {payload}")
                else:
                    logger.error(f"topo client sends heartbeat failed. register_url: {register_url}, data: {payload}")
            except Exception as e:
                logger.error(f"Error while registering instance {worker_dict}: {e}")
                traceback.print_exception(e)
        logger.info("heartbeat service stop successfully")
