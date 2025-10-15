# Patio Documentation

Welcome to the documentation for Patio, a Python runtime component for the RBGS (Role-Based Group Scheduling) system.

## Table of Contents

1. [API Protocol](api_protocol.md) - Data models used in the API
2. [Engine Components](engine.md) - Inference engine interfaces and implementations
3. [Metrics](metrics.md) - Prometheus metrics collection and exposition
4. [Topology](topo.md) - Distributed worker topology management

## Overview

Patio provides a FastAPI-based server that acts as an interface between the RBG controller and inference engines like SGLang and Others. It handles LoRA adapter management, metrics collection, and topology management for distributed inference workloads.

## Getting Started

For installation and basic usage instructions, see the [README](../README.md).

## Contributing

See the [development guide](../README.md#development) in the README for information on how to contribute to Patio.