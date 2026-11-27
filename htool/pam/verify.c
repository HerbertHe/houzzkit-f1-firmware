#define _POSIX_C_SOURCE 200809L
#include <dlfcn.h>
#include <security/pam_appl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static int conversation(int count, const struct pam_message **messages,
                        struct pam_response **response, void *data)
{
    if (count < 1 || count > 8)
        return PAM_CONV_ERR;
    struct pam_response *answers = calloc((size_t)count, sizeof(*answers));
    if (!answers)
        return PAM_BUF_ERR;
    for (int i = 0; i < count; ++i) {
        switch (messages[i]->msg_style) {
        case PAM_PROMPT_ECHO_OFF:
            answers[i].resp = strdup(data);
            if (!answers[i].resp)
                goto fail;
            break;
        case PAM_TEXT_INFO:
        case PAM_ERROR_MSG:
            break;
        default:
            goto fail;
        }
    }
    *response = answers;
    return PAM_SUCCESS;
fail:
    for (int i = 0; i < count; ++i)
        free(answers[i].resp);
    free(answers);
    return PAM_CONV_ERR;
}

int main(void)
{
    char password[513];
    if (geteuid() != 0)
        return 2;
    size_t length = fread(password, 1, sizeof(password), stdin);
    if (ferror(stdin) || length == 0 || length == sizeof(password) ||
        memchr(password, 0, length))
        return 2;
    password[length] = 0;

    void *lib = dlopen("libpam.so.0", RTLD_NOW | RTLD_LOCAL);
    if (!lib)
        return 2;
    int (*start)(const char *, const char *, const struct pam_conv *, pam_handle_t **) =
        dlsym(lib, "pam_start");
    int (*authenticate)(pam_handle_t *, int) = dlsym(lib, "pam_authenticate");
    int (*account)(pam_handle_t *, int) = dlsym(lib, "pam_acct_mgmt");
    int (*end)(pam_handle_t *, int) = dlsym(lib, "pam_end");
    if (!start || !authenticate || !account || !end)
        return 2;

    struct pam_conv conv = {conversation, password};
    pam_handle_t *handle = NULL;
    int status = start("htoold", "root", &conv, &handle);
    if (status == PAM_SUCCESS) {
        status = authenticate(handle, PAM_SILENT | PAM_DISALLOW_NULL_AUTHTOK);
        if (status == PAM_SUCCESS)
            status = account(handle, PAM_SILENT | PAM_DISALLOW_NULL_AUTHTOK);
        end(handle, status);
    }
    for (volatile char *p = password; p < password + sizeof(password); ++p)
        *p = 0;
    dlclose(lib);
    switch (status) {
    case PAM_SUCCESS:
        return 0;
    case PAM_AUTH_ERR:
    case PAM_CRED_INSUFFICIENT:
    case PAM_MAXTRIES:
    case PAM_USER_UNKNOWN:
    case PAM_PERM_DENIED:
    case PAM_NEW_AUTHTOK_REQD:
    case PAM_ACCT_EXPIRED:
        return 1;
    default:
        return 2;
    }
}
