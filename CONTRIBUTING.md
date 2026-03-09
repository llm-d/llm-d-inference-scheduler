## Contributing Guidelines

Thank you for your interest in contributing to this project. Community involvement is highly valued and crucial for the project's growth and success. This project accepts contributions via GitHub pull requests. This document outlines the process to help get your contribution accepted.

To ensure a clear direction and cohesive vision for the project, the project leads have the final decision on all contributions. However, these guidelines outline how you can contribute effectively.

## How You Can Contribute

There are several ways you can contribute:

* **Reporting Issues:** Help us identify and fix bugs by reporting them clearly and concisely.
* **Suggesting Features:** Share your ideas for new features or improvements.
* **Improving Documentation:** Help make the project more accessible by enhancing the documentation.
* **Submitting Code Contributions:** Code contributions that align with the project's vision are always welcome.

## Code of Conduct

This project adheres to the [Code of Conduct and Covenant](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code.

## Community and Communication

* **Developer Slack:** [Join our developer Slack workspace](https://llm-d.ai/slack) to connect with the core maintainers and other contributors, ask questions, and participate in discussions.
* **Weekly Meetings:** Project updates, ongoing work discussions, and Q&A will be covered in our weekly project meetings. Please join by [adding the shared calendar](https://red.ht/llm-d-public-calendar). You can also [join our Google Group](https://groups.google.com/g/llm-d-contributors) for access to shared content.
* **Code:** Hosted in the [llm-d](https://github.com/llm-d) GitHub organization
* **Issues:** Project-scoped bugs or issues should be reported in this repo or in [llm-d/llm-d](https://github.com/llm-d/llm-d)
* **Mailing List:** [llm-d-contributors@googlegroups.com](mailto:llm-d-contributors@googlegroups.com)

## Contributing Process

We follow a **lazy consensus** approach: changes proposed by people with responsibility for a problem, without disagreement from others, within a bounded time window of review by their peers, should be accepted.

### Types of Contributions

#### 1. Features with Public APIs or New Components

All features involving public APIs, behavior between core components, or new core subsystems must be accompanied by an **approved project proposal**.

**Process:**

1. Create a pull request adding a proposal document under `./docs/proposals/` with a descriptive name
2. Include these sections: Summary, Motivation (Goals/Non-Goals), Proposal, Design Details, Alternatives
3. Get review from impacted component maintainers
4. Get approval from project maintainers

#### 2. Fixes, Issues, and Bugs

For changes that fix broken code or add small changes within a component:

* All bugs and commits must have a clear description of the bug, how to reproduce, and how the change is made
* Any other changes can be proposed in a pull request — a maintainer must approve the change
* For moderate size changes, create an RFC issue in GitHub, then engage in Slack

## Code Review Requirements

* **All code changes** must be submitted as pull requests (no direct pushes)
* **All changes** must be reviewed and approved by a maintainer other than the author
* **All repositories** must gate merges on compilation and passing tests
* **All experimental features** must be off by default and require explicit opt-in

## Commit and Pull Request Style

* **Pull requests** should describe the problem succinctly
* **Rebase and squash** before merging
* **Use minimal commits** and break large changes into distinct commits
* **Commit messages** should have:
  * Short, descriptive titles
  * Description of why the change was needed
  * Enough detail for someone reviewing git history to understand the scope
* **DCO Sign-off**: All commits must include a valid DCO sign-off line (`Signed-off-by: Name <email@domain.com>`)
  * Add automatically with `git commit -s`
  * See [PR_SIGNOFF.md](PR_SIGNOFF.md) for configuration details
  * Required for all contributions per [Developer Certificate of Origin](https://developercertificate.org/)

## Code Organization and Ownership

* **Components** are the primary unit of code organization
* **Maintainers** own components and approve changes
* **Contributors** can become maintainers through sufficient evidence of contribution
* Code ownership is reflected in [OWNERS files](https://go.k8s.io/owners) consistent with Kubernetes project conventions

## Testing Requirements

We use three tiers of testing:

1. **Unit tests**: Fast verification of code parts, testing different arguments
2. **Integration tests**: Testing protocols between components and built artifacts
3. **End-to-end (e2e) tests**: Whole system testing including benchmarking

Strong e2e coverage is required for deployed systems to prevent performance regression. Appropriate test coverage is an important part of code review.

## Security

See [SECURITY.md](SECURITY.md) for our vulnerability disclosure process.

## API Changes and Deprecation

* **No breaking changes**: Once an API/protocol is in GA release, it cannot be removed or behavior changed
* **Versioning**: All protocols and APIs should be versionable with clear compatibility requirements
* **Documentation**: All APIs must have documented specs describing expected behavior
