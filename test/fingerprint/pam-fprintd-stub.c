/* A stand-in for the fingerprint extension's pam_fprintd.so, for
 * test/fingerprint/check-fingerprint.sh: it answers what /run/fp-stub says
 * ("match": a finger matched; anything else: no reader, as pam_fprintd says
 * when there is no reader or no finger enrolled) and appends one line to
 * /run/fp-stub.calls each time PAM asks it. */
#include <security/pam_modules.h>
#include <stdio.h>
#include <string.h>

PAM_EXTERN int pam_sm_authenticate(pam_handle_t *h, int flags, int argc, const char **argv)
{
	(void)h; (void)flags; (void)argc; (void)argv;
	char mode[32] = "";
	FILE *f = fopen("/run/fp-stub", "r");
	if (f != NULL) {
		if (fgets(mode, sizeof mode, f) == NULL)
			mode[0] = '\0';
		fclose(f);
	}
	f = fopen("/run/fp-stub.calls", "a");
	if (f != NULL) {
		fputs("called\n", f);
		fclose(f);
	}
	return strncmp(mode, "match", 5) == 0 ? PAM_SUCCESS : PAM_AUTHINFO_UNAVAIL;
}

PAM_EXTERN int pam_sm_setcred(pam_handle_t *h, int flags, int argc, const char **argv)
{
	(void)h; (void)flags; (void)argc; (void)argv;
	return PAM_SUCCESS;
}
