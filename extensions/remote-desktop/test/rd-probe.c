/*
 * rd-probe: the test stage's VNC client, for the remote-desktop extension
 * only (never shipped). It checks what a viewer meets: which RFB security
 * types the server offers, and the VeNCrypt X509Plain login through TLS.
 *
 *   rd-probe gen DIR                    write rsa-key.pem (PKCS #1),
 *                                       tls-key.pem (SEC 1 EC) and
 *                                       tls-cert.pem, the formats
 *                                       `isotope remote-desktop` writes
 *   rd-probe types HOST PORT            print the offered security types
 *   rd-probe vencrypt HOST PORT USER PW print the VeNCrypt subtypes, log in;
 *                                       exit 0 accepted, 1 refused,
 *                                       2 anything else
 */
#include <arpa/inet.h>
#include <gnutls/gnutls.h>
#include <gnutls/x509.h>
#include <netdb.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

static void die(const char *msg) {
	fprintf(stderr, "rd-probe: %s\n", msg);
	exit(2);
}

static void check(int rc, const char *what) {
	if (rc < 0) {
		fprintf(stderr, "rd-probe: %s: %s\n", what, gnutls_strerror(rc));
		exit(2);
	}
}

static void readn(int fd, void *buf, size_t n) {
	size_t got = 0;
	while (got < n) {
		ssize_t r = read(fd, (char *)buf + got, n - got);
		if (r <= 0)
			die("the server closed the connection");
		got += (size_t)r;
	}
}

static void writen(int fd, const void *buf, size_t n) {
	if (write(fd, buf, n) != (ssize_t)n)
		die("write failed");
}

static int dial(const char *host, const char *port) {
	struct addrinfo hints = {.ai_socktype = SOCK_STREAM}, *res;
	if (getaddrinfo(host, port, &hints, &res) != 0)
		die("cannot resolve the address");
	int fd = socket(res->ai_family, res->ai_socktype, res->ai_protocol);
	if (fd < 0 || connect(fd, res->ai_addr, res->ai_addrlen) != 0)
		die("cannot connect");
	freeaddrinfo(res);
	return fd;
}

/* handshake reads the version, answers 3.8, and returns the security
 * types the server offers (n of them in types). */
static int handshake(int fd, uint8_t *types) {
	char ver[12];
	readn(fd, ver, sizeof ver);
	if (memcmp(ver, "RFB 003.", 8) != 0)
		die("not an RFB server");
	writen(fd, "RFB 003.008\n", 12);
	uint8_t n;
	readn(fd, &n, 1);
	if (n == 0)
		die("the server offers no security type");
	readn(fd, types, n);
	return n;
}

static void write_pem(const char *dir, const char *name, gnutls_datum_t *d) {
	char path[4096];
	snprintf(path, sizeof path, "%s/%s", dir, name);
	FILE *f = fopen(path, "w");
	if (!f || fwrite(d->data, 1, d->size, f) != d->size || fclose(f) != 0)
		die("cannot write a key file");
	gnutls_free(d->data);
}

static int gen(const char *dir) {
	gnutls_x509_privkey_t rsa, ec;
	gnutls_x509_crt_t crt;
	gnutls_datum_t out;
	check(gnutls_x509_privkey_init(&rsa), "init");
	check(gnutls_x509_privkey_generate(rsa, GNUTLS_PK_RSA, 2048, 0), "RSA key");
	check(gnutls_x509_privkey_export2(rsa, GNUTLS_X509_FMT_PEM, &out), "RSA export");
	if (!strstr((char *)out.data, "BEGIN RSA PRIVATE KEY"))
		die("the RSA key is not PKCS #1");
	write_pem(dir, "rsa-key.pem", &out);
	check(gnutls_x509_privkey_init(&ec), "init");
	check(gnutls_x509_privkey_generate(ec, GNUTLS_PK_ECDSA, GNUTLS_CURVE_TO_BITS(GNUTLS_ECC_CURVE_SECP256R1), 0), "EC key");
	check(gnutls_x509_privkey_export2(ec, GNUTLS_X509_FMT_PEM, &out), "EC export");
	if (!strstr((char *)out.data, "BEGIN EC PRIVATE KEY"))
		die("the EC key is not SEC 1");
	write_pem(dir, "tls-key.pem", &out);
	check(gnutls_x509_crt_init(&crt), "init");
	check(gnutls_x509_crt_set_version(crt, 3), "version");
	unsigned char serial[8] = {1, 2, 3, 4, 5, 6, 7, 8};
	check(gnutls_x509_crt_set_serial(crt, serial, sizeof serial), "serial");
	check(gnutls_x509_crt_set_activation_time(crt, time(NULL) - 3600), "activation");
	check(gnutls_x509_crt_set_expiration_time(crt, time(NULL) + 3600 * 24 * 365), "expiration");
	check(gnutls_x509_crt_set_dn_by_oid(crt, GNUTLS_OID_X520_COMMON_NAME, 0, "isotope-test", 12), "name");
	check(gnutls_x509_crt_set_key(crt, ec), "key");
	check(gnutls_x509_crt_sign2(crt, crt, ec, GNUTLS_DIG_SHA256, 0), "sign");
	check(gnutls_x509_crt_export2(crt, GNUTLS_X509_FMT_PEM, &out), "certificate export");
	write_pem(dir, "tls-cert.pem", &out);
	return 0;
}

static int types(const char *host, const char *port) {
	uint8_t t[256];
	int fd = dial(host, port), n = handshake(fd, t);
	for (int i = 0; i < n; i++)
		printf("%u\n", t[i]);
	close(fd);
	return 0;
}

static ssize_t tls_push(gnutls_transport_ptr_t p, const void *b, size_t n) {
	return write((int)(intptr_t)p, b, n);
}

static ssize_t tls_pull(gnutls_transport_ptr_t p, void *b, size_t n) {
	return read((int)(intptr_t)p, b, n);
}

static void tls_readn(gnutls_session_t s, void *buf, size_t n) {
	size_t got = 0;
	while (got < n) {
		ssize_t r = gnutls_record_recv(s, (char *)buf + got, n - got);
		if (r == GNUTLS_E_AGAIN || r == GNUTLS_E_INTERRUPTED)
			continue;
		if (r <= 0)
			die("the server closed the TLS session");
		got += (size_t)r;
	}
}

static int vencrypt(const char *host, const char *port, const char *user, const char *pw) {
	uint8_t t[256];
	int fd = dial(host, port), n = handshake(fd, t), found = 0;
	for (int i = 0; i < n; i++)
		found |= t[i] == 19;
	if (!found)
		die("no VeNCrypt (19) offered");
	uint8_t b = 19, v[2];
	writen(fd, &b, 1);
	readn(fd, v, 2);
	if (v[0] != 0 || v[1] != 2)
		die("not VeNCrypt 0.2");
	writen(fd, v, 2);
	readn(fd, &b, 1);
	if (b != 0)
		die("the server refused VeNCrypt 0.2");
	uint8_t ns;
	readn(fd, &ns, 1);
	int x509plain = 0;
	for (int i = 0; i < ns; i++) {
		uint32_t st;
		readn(fd, &st, 4);
		st = ntohl(st);
		printf("subtype %u\n", st);
		x509plain |= st == 262;
	}
	if (!x509plain)
		die("no X509Plain (262) subtype");
	uint32_t choice = htonl(262);
	writen(fd, &choice, 4);
	readn(fd, &b, 1);
	if (b != 1)
		die("the server did not accept X509Plain");

	gnutls_session_t s;
	gnutls_certificate_credentials_t cred;
	check(gnutls_certificate_allocate_credentials(&cred), "credentials");
	check(gnutls_init(&s, GNUTLS_CLIENT), "init");
	check(gnutls_set_default_priority(s), "priority");
	check(gnutls_credentials_set(s, GNUTLS_CRD_CERTIFICATE, cred), "credentials");
	gnutls_transport_set_ptr(s, (gnutls_transport_ptr_t)(intptr_t)fd);
	gnutls_transport_set_push_function(s, tls_push);
	gnutls_transport_set_pull_function(s, tls_pull);
	int rc;
	do
		rc = gnutls_handshake(s);
	while (rc < 0 && !gnutls_error_is_fatal(rc));
	check(rc, "TLS handshake");
	printf("tls %s\n", gnutls_protocol_get_name(gnutls_protocol_get_version(s)));

	uint32_t ul = strlen(user), pl = strlen(pw), lens[2] = {htonl(ul), htonl(pl)};
	size_t len = 8 + ul + pl;
	char *msg = malloc(len);
	memcpy(msg, lens, 8);
	memcpy(msg + 8, user, ul);
	memcpy(msg + 8 + ul, pw, pl);
	if (gnutls_record_send(s, msg, len) != (ssize_t)len)
		die("cannot send the login");
	free(msg);
	uint32_t result;
	tls_readn(s, &result, 4);
	result = ntohl(result);
	printf("result %u\n", result);
	gnutls_bye(s, GNUTLS_SHUT_RDWR);
	close(fd);
	return result == 0 ? 0 : 1;
}

int main(int argc, char **argv) {
	check(gnutls_global_init(), "global init");
	if (argc == 3 && strcmp(argv[1], "gen") == 0)
		return gen(argv[2]);
	if (argc == 4 && strcmp(argv[1], "types") == 0)
		return types(argv[2], argv[3]);
	if (argc == 6 && strcmp(argv[1], "vencrypt") == 0)
		return vencrypt(argv[2], argv[3], argv[4], argv[5]);
	fprintf(stderr, "usage: rd-probe gen DIR | types HOST PORT | vencrypt HOST PORT USER PASSWORD\n");
	return 2;
}
