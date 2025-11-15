
# Contributing to RBG

Welcome to **RBG (RoleBasedGroup)**! 🎉  
We would greatly appreciate it if you could contribute to make RBG better.  
This document aims to give you some basic instructions on how to make your contribution accepted by RBG.

---

## Code of Conduct

Before making any contribution, please have a look at our [code of conduct](doc/code-of-conduct.md), which details how contributors are expected to conduct themselves as part of the RBG community.

---

## Filing Issues

Issues might be the most common way to get involved in the RBG project. Whether you are a user or a developer, you may have some feedback based on daily use or some great ideas that may improve RBG, and that's where "issues" come into play. We'd be very glad to hear your voices, so feel free to file issues to Fluid.

Typical reasons to open an issue include (but are not limited to):

- Find a bug and want to report it
- Want help
- Find documentation is incomplete or unclear
- Find test cases that can be improved
- Want a new feature
- Propose a new feature design, including new architecture or API changes
- Performance issues
- General questions about the project
- …

⚠️ **Please make sure all sensitive data is removed** before filing an issue (e.g., passwords, secret keys, network locations, internal business data).

You can file issues here: [RBG Issues](https://github.com/sgl-project/rbg/issues)

---

## Code Contributions

RBG accepts code contributions via GitHub **pull requests (PR)**, and this is the only accepted way to submit your changes.

You should submit a PR for **any modification**, including but not limited to:

- Fix typos
- Fix bugs
- Fix or polish documents
- Prune redundant code
- Add or improve code comments
- Add missing test cases
- Add new features or enhance some feature
- Refactor code
- Improve performance
- ...

The following is a step-by-step guide for contributing code to RBG.

---

### Setting up Development Workspace

We assume you already have a GitHub account.  

1. **Fork the RBG repository**  

    Click the **"Fork"** button on the [RBG GitHub page](https://github.com/sgl-project/rbg). You will get a forked repository that you fully control.

2. **Clone your forked repository**  
    Clone the forked repository to your local machine.

    ```shell
    git clone https://github.com/<your-username>/rbg.git
    ```

3. **Set upstream remote**  

    ```shell
    cd rbg
    git remote add upstream https://github.com/sgl-project/rbg.git
    git remote set-url --push upstream no-pushing
    ```

4. **Sync your local code with upstream**  

    ```shell
    git fetch upstream
    git checkout main
    git rebase upstream/main
    ```

5. **Create a new branch for your work**  

    ```shell
    git checkout -b <new-branch>
    ```

    Make changes in the `<new-branch>`. For more about RBG development, please see the [Developer Guide](doc/dev/how_to_develop.md)

---

### Developer Certificate of Origin (DCO)

The Developer Certificate of Origin (DCO) is a lightweight way for contributors to certify that they wrote or otherwise have the right to submit the code they are contributing to the project.

RBG project requires a **DCO sign-off** for commit messages to certify that you wrote or have the right to submit the code.

Example:

```text
This is my commit message

Signed-off-by: Random J Developer <random@developer.example.org>
```

Git has a helpful `-s` option to add it automatically:

```shell
git commit -s -m 'This is my commit message'
```

If you have already made a commit and forgot to include the sign-off, you can amend your last commit to add the sign-off with the following command, which can then be force pushed.

```shell
git commit --amend -s
```

---

### Submitting Pull Requests

Once you've done your work on developing RGB, you are now ready to submit a PR to the RGB project.

1. Push your branch to your forked repository:

    ```shell
    git push origin <new-branch>
    ```

2. Go to your forked repository on GitHub, open the **"Pull requests"** tab, click **"New Pull Request"**, choose `<new-branch>`, check your changes, fill in the PR title and description, and click **"Create pull request"**.

3. To help reviewers better get your purpose, please ensure:
   - PR titles are descriptive but concise.
   - Follow the [PR Template](.github/PULL_REQUEST_TEMPLATE.md).
   - Ensure all CI tests pass.

---

### Tracking Your PR

- After you submit a PR, maintainers will review your changes.
- Please keep track of the review status, answer reviewers' questions, and update your PR as needed.
- After at least one maintainer approval, your PR will be merged into the RBG codebase.  
  🎉 Congratulations, you’ve contributed to RBG! Thank you for your help and welcome back anytime!

---

## Any Help Is Contribution

Though contributions via Github PR is an explicit way to help, we still call for any other help:

- Answer other users’ issues
- Help solve problems for others
- Review PRs
- Participate in discussions
- Promote RBG outside GitHub
- Write blog posts or tutorials about RBG
- Share RBG usage patterns and best practices
- …

---

## Join Our Community

You are warmly welcome if you'd like to join our community as a member. Together we can make this community even better!

### Some requirements are needed to join our community

- Read [this contributing guide](CONTRIBUTING.md) carefully
- Promise to comply with our [Code of Conduct](doc/code-of-conduct.md)
- Submit multiple PRs to RBG
- Be active in the community, including but not limited to:
  - Open new issues
  - Participate in discussions
  - Review PRs
  - Submit PRs  

### How to join

You can do it in either of the following two ways:

- Submit a PR to introduce yourself to the community  
- Or contact us via [Slack Channel](https://sgl-fru7574.slack.com/archives/C098X0LQZV5)

---

💡 **Tip:** Whether you're fixing a small typo or proposing a major feature — every contribution is valuable.  
Thank you for helping make **RBG** better! 🚀
