#!/usr/bin/env python3
# Copyright 2026 The RBG Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Optuna bridge for auto-benchmark parameter tuning.

Long-running subprocess communicating with Go controller via JSON Lines
over stdin/stdout. Logs go to stderr.

Supported actions:
  init     - Create or load an Optuna study
  ask      - Get next suggested parameters
  tell     - Report trial results
  is_done  - Check if search should stop
  best     - Get the best feasible trial
  shutdown - Gracefully stop the bridge
"""

import json
import logging
import os
import sys
import threading
import time

import optuna
from optuna.distributions import CategoricalDistribution
from optuna.trial import TrialState

logging.basicConfig(
    stream=sys.stderr,
    level=logging.INFO,
    format="%(asctime)s [optuna-bridge] %(levelname)s %(message)s",
)
logger = logging.getLogger("optuna_bridge")

# Suppress Optuna's verbose logging.
optuna.logging.set_verbosity(optuna.logging.WARNING)

# Sampler registry: algorithm name -> Optuna sampler class.
SAMPLER_MAP = {
    "tpe": optuna.samplers.TPESampler,
    "gp": optuna.samplers.GPSampler,
    "cmaes": optuna.samplers.CmaEsSampler,
    "random": optuna.samplers.RandomSampler,
    "qmc": optuna.samplers.QMCSampler,
    "nsgaii": optuna.samplers.NSGAIISampler,
    "nsgaiii": optuna.samplers.NSGAIIISampler,
}

# Samplers that support the constraints_func parameter for SLA constraints.
CONSTRAINT_CAPABLE = {"tpe", "nsgaii", "nsgaiii"}


def _create_sampler(
    name: str,
    seed: int | None,
    constraints_func,
    kwargs: dict | None,
) -> optuna.samplers.BaseSampler:
    """Dynamically create an Optuna sampler by name with optional kwargs."""
    cls = SAMPLER_MAP.get(name)
    if cls is None:
        raise ValueError(
            f"unknown sampler: {name!r}, supported: {sorted(SAMPLER_MAP)}"
        )
    kw = dict(kwargs or {})
    if seed is not None:
        kw["seed"] = seed
    if name in CONSTRAINT_CAPABLE:
        kw["constraints_func"] = constraints_func
    return cls(**kw)


class StudyManager:
    """Manages multiple Optuna studies (one per template)."""

    def __init__(self):
        self.studies: dict[str, optuna.Study] = {}
        self.distributions: dict[str, dict[str, CategoricalDistribution]] = {}
        self.pending_trials: dict[str, optuna.Trial | None] = {}
        self.max_trials: dict[str, int] = {}
        self.space_sizes: dict[str, int] = {}  # Cartesian product size per study

    def init_study(
        self,
        study_name: str,
        search_space: dict[str, dict[str, list]],
        direction: str = "maximize",
        max_trials: int = 100,
        storage_path: str | None = None,
        seed: int | None = None,
        sampler: str = "tpe",
        sampler_kwargs: dict | None = None,
    ) -> dict:
        """Create or load an Optuna study."""

        def constraints_func(trial: optuna.trial.FrozenTrial) -> list[float]:
            if "constraints" not in trial.user_attrs:
                raise ValueError(
                    f"Trial {trial.number} missing 'constraints' user attr"
                )
            return trial.user_attrs["constraints"]

        samp = _create_sampler(sampler, seed, constraints_func, sampler_kwargs)

        storage = f"sqlite:///{storage_path}" if storage_path else None

        study = optuna.create_study(
            study_name=study_name,
            storage=storage,
            sampler=samp,
            direction=direction,
            load_if_exists=True,
        )

        # Prune any stale RUNNING trials from a previous crashed run.
        for t in study.trials:
            if t.state == TrialState.RUNNING:
                study.tell(t.number, state=TrialState.FAIL)
                logger.info("Pruned stale RUNNING trial #%d", t.number)

        # Build flat distributions: "role/param" -> CategoricalDistribution.
        distributions = {}
        for role, params in search_space.items():
            for param_name, values in params.items():
                flat_key = f"{role}/{param_name}"
                typed_values = _normalize_values(values)
                distributions[flat_key] = CategoricalDistribution(choices=typed_values)

        self.studies[study_name] = study
        self.distributions[study_name] = distributions
        self.max_trials[study_name] = max_trials
        self.pending_trials[study_name] = None

        # Compute Cartesian product size of the search space.
        space_size = 1
        for dist in distributions.values():
            space_size *= len(dist.choices)
        self.space_sizes[study_name] = space_size

        existing_total = sum(
            1 for t in study.trials if t.state != TrialState.RUNNING
        )
        logger.info(
            "Initialized study %r: sampler=%s, %d total trials, max=%d, space_size=%d",
            study_name,
            sampler,
            existing_total,
            max_trials,
            space_size,
        )
        return {
            "status": "ok",
            "existing_total": existing_total,
            "space_size": space_size,
        }

    def ask(self, study_name: str) -> dict:
        """Ask Optuna for the next parameter suggestion."""
        study = self.studies[study_name]
        dists = self.distributions[study_name]

        trial = study.ask(dists)
        self.pending_trials[study_name] = trial

        # Unflatten "role/param" back to nested dict.
        params: dict[str, dict[str, object]] = {}
        for flat_key, value in trial.params.items():
            role, param_name = flat_key.split("/", 1)
            params.setdefault(role, {})[param_name] = value

        logger.info("Study %r: asked trial #%d", study_name, trial.number)
        return {"status": "ok", "trial_id": trial.number, "params": params}

    def tell(
        self,
        study_name: str,
        trial_id: int,
        score: float,
        sla_pass: bool,
        error: str = "",
    ) -> dict:
        """Report trial results to Optuna."""
        study = self.studies[study_name]

        # Idempotent: skip if already told (restart scenario).
        trials_by_number = {t.number: t for t in study.trials}
        existing = trials_by_number.get(trial_id)
        if existing is not None and existing.state in (
            TrialState.COMPLETE,
            TrialState.FAIL,
        ):
            logger.info(
                "Study %r: trial #%d already %s, skipping tell",
                study_name,
                trial_id,
                existing.state.name,
            )
            return {"status": "ok", "skipped": True}

        pending = self.pending_trials.get(study_name)

        if error:
            if pending is not None and pending.number == trial_id:
                study.tell(pending, state=TrialState.FAIL)
            else:
                study.tell(trial_id, state=TrialState.FAIL)
            logger.info(
                "Study %r: trial #%d FAILED: %s", study_name, trial_id, error
            )
        else:
            constraint_value = 0.0 if sla_pass else 1.0
            if pending is not None and pending.number == trial_id:
                # Normal path: pending Trial has storage reference, set attr before tell.
                pending.set_user_attr("constraints", [constraint_value])
                study.tell(pending, score)
            else:
                # Resumed trial path: set constraints via storage internals BEFORE tell.
                # This is required because constraints_func (used by TPE/NSGA-II samplers)
                # is called inside tell()'s after_trial callback and needs user_attrs["constraints"].
                #
                # Optuna Issue #3640 confirms that cross-process ask-tell with constrained
                # optimization requires restoring sampler state before tell. Since our bridge
                # process persists across Go controller restarts, the sampler state is intact,
                # but we lack the Trial object to set user_attrs via public API.
                #
                # Using _storage internals is a known limitation. If this breaks on future
                # Optuna versions, the alternative is to pickle Trial objects on ask and
                # restore them on tell, which requires significant architectural changes.
                try:
                    trial_id_in_storage = (
                        study._storage.get_trial_id_from_study_id_trial_number(
                            study._study_id, trial_id
                        )
                    )
                    study._storage.set_trial_user_attr(
                        trial_id_in_storage, "constraints", [constraint_value]
                    )
                except AttributeError as e:
                    raise RuntimeError(
                        f"Optuna internal API changed: {e}. "
                        f"This bridge requires Optuna 3.x/4.x storage internals. "
                        f"Please report this issue with your Optuna version."
                    ) from e
                study.tell(trial_id, score, skip_if_finished=True)

            logger.info(
                "Study %r: trial #%d score=%.4f sla_pass=%s",
                study_name,
                trial_id,
                score,
                sla_pass,
            )

        self.pending_trials[study_name] = None
        return {"status": "ok"}

    def is_done(self, study_name: str) -> dict:
        """Check if search should stop.

        Done when any of:
        - completed trials >= max_trials
        - completed trials >= search space Cartesian product size (exhausted)
        """
        study = self.studies[study_name]
        completed = sum(
            1
            for t in study.trials
            if t.state == TrialState.COMPLETE
        )
        max_t = self.max_trials.get(study_name, 0)
        space_size = self.space_sizes.get(study_name, 0)
        done = completed >= max_t or (space_size > 0 and completed >= space_size)
        return {
            "status": "ok",
            "done": done,
            "completed": completed,
            "space_size": space_size,
        }

    def best(self, study_name: str) -> dict:
        """Get the best feasible trial."""
        study = self.studies[study_name]
        try:
            best_trial = study.best_trial
            params: dict[str, dict[str, object]] = {}
            for flat_key, value in best_trial.params.items():
                role, param_name = flat_key.split("/", 1)
                params.setdefault(role, {})[param_name] = value
            return {
                "status": "ok",
                "trial_id": best_trial.number,
                "score": best_trial.value,
                "params": params,
            }
        except ValueError:
            return {"status": "error", "message": "no completed feasible trials"}


def _normalize_values(values: list) -> list:
    """Normalize values for Optuna CategoricalDistribution (must be hashable)."""
    result = []
    for v in values:
        if isinstance(v, bool):
            result.append(v)
        elif isinstance(v, float):
            result.append(int(v) if v == int(v) else v)
        elif isinstance(v, int):
            result.append(v)
        else:
            result.append(str(v))
    return result


def _watch_parent():
    """Exit the bridge if the parent process dies (e.g., Go controller crash)."""
    original_ppid = os.getppid()
    while True:
        time.sleep(10)
        if os.getppid() != original_ppid:
            logger.info(
                "Parent process changed (was %d, now %d), shutting down",
                original_ppid,
                os.getppid(),
            )
            os._exit(1)


def main():
    manager = StudyManager()
    logger.info("Optuna bridge started, waiting for commands...")

    # Daemon thread: if Go controller crashes, PPID changes and we exit.
    threading.Thread(target=_watch_parent, daemon=True).start()

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            request = json.loads(line)
        except json.JSONDecodeError as e:
            _respond({"status": "error", "message": f"invalid JSON: {e}"})
            continue

        action = request.get("action", "")
        study_name = request.get("study_name", "")

        try:
            if action == "init":
                response = manager.init_study(
                    study_name=study_name,
                    search_space=request["search_space"],
                    direction=request.get("direction", "maximize"),
                    max_trials=request.get("max_trials", 100),
                    storage_path=request.get("storage_path"),
                    seed=request.get("seed"),
                    sampler=request.get("sampler", "tpe"),
                    sampler_kwargs=request.get("sampler_kwargs"),
                )
            elif action == "ask":
                response = manager.ask(study_name)
            elif action == "tell":
                response = manager.tell(
                    study_name=study_name,
                    trial_id=request["trial_id"],
                    score=request.get("score", 0.0),
                    sla_pass=request.get("sla_pass", False),
                    error=request.get("error", ""),
                )
            elif action == "is_done":
                response = manager.is_done(study_name)
            elif action == "best":
                response = manager.best(study_name)
            elif action == "shutdown":
                logger.info("Shutdown requested")
                _respond({"status": "ok"})
                break
            else:
                response = {
                    "status": "error",
                    "message": f"unknown action: {action}",
                }
        except Exception as e:
            logger.exception("Error handling action %r", action)
            response = {"status": "error", "message": str(e)}

        _respond(response)

    logger.info("Optuna bridge exiting")


def _respond(data: dict):
    sys.stdout.write(json.dumps(data, default=str) + "\n")
    sys.stdout.flush()


if __name__ == "__main__":
    main()
