# Quay CI App

Quay CI App is a GitHub App that syncs branches between repositories.

## How to run the app

### Create a GitHub App

Open [GitHub Apps](https://github.com/settings/apps) for your GitHub account. If you don't have the Quay CI app there, click on `New GitHub App`.

Fill in the form with the following values:

- **GitHub App name:** Quay CI (Oleg version)
- **Homepage URL:** https://github.com/quay/quay-ci-app
- **Webhook URL:** An endpoint of your instance. For example, if [you IP address](https://www.google.com/search?q=my+ip+address) is 203.0.113.151 and you run the app locally, the endpoint will be http://203.0.113.151:8080. If you don't have a public IP address, you can use [ngrok](https://ngrok.com/) to create a tunnel to your local machine
- **Repository permissions:**
    - **Administration:** Read and write (if needed by branch protection)
    - **Checks:** Read and write
    - **Contents:** Read and write
    - **Issues:** Read and write
    - **Metadata:** Read-only
    - **Pull requests:** Read and write
- **Subscribe to events:**
    - Check run
    - Issue comment
    - Issues
    - Pull request
    - Push

Click on `Create GitHub App`. This will redirect you to the app page.

On the app page, you'll find **App ID** that'll you need for your config file.

Click on `Generate a private key` to generate and download a **private key** that you will need to run the app.

Click on `Install App`, then `Install` to install the app on your account. Once the app is installed, you will be redirected to the installation page. The URL for this page will contain the **installation ID** that you need for your config file.

### Create a config file

To keep the branch `test-release` synced with the branch `master` of the repository `dmage/quay`, create a file `config.yaml` with the following content:

```yaml
app_id: 275455
installation_id: 32491971
repositories:
- owner: dmage
  repo: quay
  jira:
    key: PROJQUAY
  branches:
  - name: test-release
    syncFrom:
      branch: master
```

### Create a Jira token

You can create a token in Jira by going to [Profile -> Personal Access Tokens](https://issues.redhat.com/secure/ViewProfile.jspa?selectedTab=com.atlassian.pats.pats-plugin:jira-user-personal-access-tokens).

[Profile -> Personal Access Tokens](https://issues.redhat.com/secure/ViewProfile.jspa?selectedTab=com.atlassian.pats.pats-plugin:jira-user-personal-access-tokens)


### Run the app

```bash
cd quay-ci-app
make
./quay-ci-app -config config.yaml -private-key /path/to/private-key.pem -v 4
```