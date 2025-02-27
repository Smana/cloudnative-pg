# Release procedure

This section describes how to release a new set of supported versions of
CloudNativePG, and should be done by one of the maintainers of the project.  It
is a semi-automated process which requires human supervision.

You can only release from a release branch, that is a branch in the
Git repository called `release-X.Y`, i.e. `release-1.16`, which corresponds
to a minor release.

The release procedure must be repeated for all the supported minor releases,
usually 3:

- the current one (`release-X.Y`)
- the previous one (`release-X.Y` -1)
- the *"End of Life"* one (`release-X.Y` -2) - normally for an additional month
  after the first release of the current minor.

```diagram
------+---------------------------------------------> main (trunk development)
       \             \
        \             \
         \             \             LATEST RELEASE
          \             \                                           ^
           \             \----------+---------------> release-X.Y   |
            \                                                       | SUPPORTED
             \                                                      | RELEASES
              \                                                     | = the two
               \                                                    |   last
                +-------------------+---------------> release-X.Y-1 |   releases
                                                                    v
```

## Release branches

A release branch must always originate from the *trunk* (`main` branch),
and corresponds to a new minor release.

Development happens on the trunk (`main` branch), and bug fixes are
cherry-picked in the actively supported release branches by the maintainers.
Sometimes, bug fixes might originate in the release branch as well.
Release notes for patch/security versions are maintained in the release branch
directly.

### Creating a new release branch from main

A new release branch is created starting from the most updated commit in the
trunk by a maintainer:

```bash
git checkout main
git pull --rebase
git checkout -b release-X.Y
git push --set-upstream origin release-X.Y
```

## Planning the release

One or two weeks before the release, you should start planning the following
activities:

- **Supported releases:** Make sure that you update the supported releases' page
  in `docs/src/supported_releases.md` and have been approved by the maintainers

- **Release notes:** Make sure release notes for the release have been updated
  in `docs/src/release_notes.md` and have been approved by the maintainers

- **Capabilities page:** in case of a new minor release, make sure that the
  operator capability levels page has been updated in
  `docs/src/operator_capability_levels.md` and approved by the maintainers

- **Documentation:** Make sure that you update the documentation in the
  [website project](https://github.com/cloudnative-pg/cloudnative-pg.github.io)
  for each of the supported releases via a PR.

<!-- TODO: we should create an issue template with a checklist for the release process -->

## Release steps

Once the code in the release branch is stable and ready to be released, you can
proceed with the supervised process.

**IMPORTANT:** You need to operate on an existing release branch. If you are
releasing a new minor version, you must create the new release branch
immediately before proceeding with the instructions below. In order to create
a new release branch, see "Creating a new release branch from main" above.

As a maintainer, you need to repeat this process for each of the supported
releases of CloudNativePG:

1. Run `hack/release.sh X.Y.Z` (e.g. `hack/release.sh 1.16.0`)
2. Quickly review the PR that is automatically generated by the script and
   approve it
3. Merge the PR, making sure that the commit message title is:
   `Version tag to X.Y.Z`, without prefixes (e.g.: `Version tag to 1.16.0`)
4. Wait until all [GitHub Actions](https://github.com/cloudnative-pg/cloudnative-pg/actions)
   complete successfully.
5. Perform manual smoke tests to verify that installation instructions work on
   your workstation using `kind`
6. In case of a new minor release, merge the new release commit on `main` with
   `git merge --ff-only release-X.Y` followed by `git push`

## Helm chart release:

After having created a new release of CloudNativePG you need to create a release of
the `cloudnative-pg` and `cnpg-sandbox` charts, whose definitions can be found
in the [cloudnative-pg/charts](https://github.com/cloudnative-pg/charts) repository.

The following is a summary of the steps to be taken in that direction. The
[RELEASE.md](https://github.com/cloudnative-pg/charts/blob/a47596cb/RELEASE.md)
document inside the relative repo contains an in-depth discussion of the
process.

1. Copy the output of `kustomize build config/helm` to `charts/cloudnative-pg/templates/crds/crds.yaml`
   in the [cloudnative-pg/charts](https://github.com/cloudnative-pg/charts) repository.
2. Diff the new release version with the previous one
   (e.g.: `vimdiff releases/cnpg-1.15.0.yaml releases/cnpg-1.15.1.yaml` using your IDE of choice)
3. Port any diff to the templates in the helm chart accordingly
4. Proceed with the release process described in the `RELEASE.md`
   file in the [cloudnative-pg/charts](https://github.com/cloudnative-pg/charts) repository.
