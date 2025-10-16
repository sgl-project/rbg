# -*- coding: utf-8 -*-
# @Author: zibai.gj


from abc import abstractmethod
from typing import Iterable

from prometheus_client import Metric
from prometheus_client.samples import Sample


class StandardRule:
    def __init__(self, rule_type):
        self.rule_type = rule_type

    @abstractmethod
    def __call__(self, metric: Metric) -> Iterable[Metric]:
        pass


class RenameStandardRule(StandardRule):
    def __init__(self, old_name, new_name):
        super().__init__("rename")
        self.old_name = old_name
        self.new_name = new_name

    def __call__(self, metric: Metric) -> Iterable[Metric]:
        assert metric.name == self.old_name, (
            f"Metric name {metric.name} does not match Rule original name {self.old_name}"
        )
        metric.name = self.new_name

        # rename all the samples
        _samples = []
        for s in metric.samples:
            s_name = self.new_name + s.name[len(self.old_name):]
            _samples.append(
                Sample(
                    s_name,
                    s.labels,
                    s.value,
                    s.timestamp,
                    s.exemplar,
                )
            )
        metric.samples = _samples
        yield metric
