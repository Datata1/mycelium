import { createHash } from "crypto";

/** Represents an authenticated user in the system. */
export interface User {
  id: string;
  email: string;
}

/** Hashes a password with SHA-256 for comparison against stored credentials. */
export function hashPassword(password: string): string {
  return createHash("sha256").update(password).digest("hex");
}

/** Normalizes an email for case-insensitive comparison. */
export function normalizeEmail(email: string): string {
  return email.toLowerCase();
}

/** AuthService validates user credentials and issues tokens. */
export class AuthService {
  private secret: string;

  constructor(secret: string) {
    this.secret = secret;
  }

  /** Checks whether a token is valid for this service. */
  async validate(token: string): Promise<boolean> {
    return token.startsWith(this.secret);
  }

  /** Issues a token for the given user. */
  issueToken(user: User): string {
    // Exercises this.<method>() — v1.3 resolver should qualify to AuthService.fingerprint.
    const fingerprint = this.fingerprint(user.email);
    return hashPassword(this.secret + fingerprint);
  }

  /** Private helper used by issueToken. Exercises this.method resolution. */
  private fingerprint(email: string): string {
    return normalizeEmail(email);
  }
}
