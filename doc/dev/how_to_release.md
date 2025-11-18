# Developer Guide

## How to release

### Helm Chart Version Updater  

**Script Path:** `rbg/tools/release/version-update.sh`  
**Purpose:** Automatically updates Helm chart versions and image tags based on Git branch names.  

---

### 🚀 Quick Start  

1. **Ensure you're on a valid version branch**:  

   ```bash
   git checkout 0.5.0        # Valid: 0.5.0, 0.5.0-alpha.1, 2.0.0-rc.1
   ```

2. **Run the script**:  

   ```bash
   ./rbg/tools/release/version-update.sh
   ```

---

### ✅ Requirements  

| Item               | Requirement                         | Example                |
|--------------------|-------------------------------------|------------------------|
| **Branch name**    | Must be a semantic version          | `0.5.0`, `1.2.3-rc.1` |
| **Makefile**       | Must contain `VERSION` variable     | `VERSION ?= v0.5.0`    |
| **File structure** | Helm charts in `deploy/helm/rbgs/`  | -                      |

---

### 🔄 What It Updates  

| File               | Field Updated       | Example Value        | Source                     |
|--------------------|---------------------|----------------------|----------------------------|
| `Chart.yaml`       | `version`          | `0.5.0-alpha.2`      | Branch name (without `v`)  |
| `Chart.yaml`       | `appVersion`       | `0.5.0-abc123`       | Image tag without `v`      |
| `values.yaml`      | `image.tag`        | `v0.5.0-abc123`      | Makefile `VERSION` + Git SHA |

---

### 🛠️ Troubleshooting  

**Common Errors:**  

```bash
# Error 1: Invalid branch name
Error: Branch 'feature/login' is not a valid version

# Solution: Rename branch to a semantic version
git branch -m 0.5.0

# Error 2: Missing files
Error: Chart.yaml not found at deploy/helm/rbgs/Chart.yaml!

# Solution: Verify chart exists at correct location
ls deploy/helm/rbgs/Chart.yaml
```

**Debug Mode:**  

```bash
# Run with detailed output
bash -x rbg/tools/release/version-update.sh
```
