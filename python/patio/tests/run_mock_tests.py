# -*- coding: utf-8 -*-
"""
Test runner script for the patio module.
"""
import subprocess
import sys
import os
from pathlib import Path


def run_tests():
    """Run all tests for the patio module."""
    # Change to the patio directory
    patio_dir = Path(__file__).parent
    os.chdir(patio_dir)
    
    # Run pytest
    try:
        result = subprocess.run([
            sys.executable, "-m", "pytest", 
            "mock_tests/",
            "-v", 
            "--tb=short"
        ], check=True, capture_output=True, text=True)
        print("Tests completed successfully!")
        print(result.stdout)
        return True
    except subprocess.CalledProcessError as e:
        print("Tests failed!")
        print(e.stdout)
        print(e.stderr)
        return False


if __name__ == "__main__":
    success = run_tests()
    sys.exit(0 if success else 1)