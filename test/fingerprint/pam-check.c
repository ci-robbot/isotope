/* pam-check SERVICE USER: runs pam_authenticate for USER through the PAM
 * service SERVICE, answering every hidden prompt with the password read from
 * standard input (one line) and a visible one with USER. Exit 0 when PAM
 * says yes, 1 when it says no, 2 when PAM could not start.
 * test/fingerprint/check-fingerprint.sh runs it in a built image. */
#include <security/pam_appl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static char password[512];
static const char *user;

static int conv(int n, const struct pam_message **msg, struct pam_response **resp, void *data)
{
	(void)data;
	struct pam_response *r = calloc((size_t)n, sizeof *r);
	if (r == NULL)
		return PAM_BUF_ERR;
	for (int i = 0; i < n; i++) {
		switch (msg[i]->msg_style) {
		case PAM_PROMPT_ECHO_OFF:
			r[i].resp = strdup(password);
			break;
		case PAM_PROMPT_ECHO_ON:
			r[i].resp = strdup(user);
			break;
		default:
			fprintf(stderr, "pam-check: %s\n", msg[i]->msg ? msg[i]->msg : "");
			break;
		}
	}
	*resp = r;
	return PAM_SUCCESS;
}

int main(int argc, char **argv)
{
	if (argc != 3) {
		fprintf(stderr, "usage: pam-check SERVICE USER < password\n");
		return 2;
	}
	user = argv[2];
	if (fgets(password, sizeof password, stdin) != NULL)
		password[strcspn(password, "\n")] = '\0';
	struct pam_conv c = { conv, NULL };
	pam_handle_t *h = NULL;
	int rc = pam_start(argv[1], user, &c, &h);
	if (rc != PAM_SUCCESS) {
		fprintf(stderr, "pam-check: pam_start: %d\n", rc);
		return 2;
	}
	rc = pam_authenticate(h, 0);
	fprintf(stderr, "pam-check: %s %s: %s\n", argv[1], user, pam_strerror(h, rc));
	pam_end(h, rc);
	return rc == PAM_SUCCESS ? 0 : 1;
}
