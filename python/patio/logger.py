# -*- coding: utf-8 -*-
# @Author: zibai.gj
# @Time  : 2025-03-06
import logging
import sys
from logging.handlers import RotatingFileHandler
from pathlib import Path


def configure_logging(log_level: str = "INFO") -> None:
    Path("/tmp/patio").mkdir(parents=True, exist_ok=True)
    logging.basicConfig(
        level=log_level,
        format="%(asctime)s - %(filename)s:%(lineno)d - %(levelname)s - %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
        handlers=[
            logging.StreamHandler(stream=sys.stdout),
            RotatingFileHandler(
                "/tmp/patio/patio.log",
                maxBytes=1024 * 1024 * 10,
                backupCount=10,
            ),
        ],
    )


def init_logger(name: str) -> logging.Logger:
    return logging.getLogger(name)
